// Package connectoragent runs outbound-only, locally configured MCP targets.
package connectoragent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/alan1-666/mcp-gateway/internal/connectorwire"
)

const maxWireBytes = 5 << 20

var targetName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var imageDigest = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*@sha256:[a-f0-9]{64}$`)

type Config struct {
	GatewayURL string         `json:"gateway_url"`
	TokenFile  string         `json:"token_file"`
	StateDir   string         `json:"state_dir"`
	Targets    []TargetConfig `json:"targets"`
}
type TargetConfig struct {
	Name         string   `json:"name"`
	Transport    string   `json:"transport"`
	URL          string   `json:"url,omitempty"`
	AllowedCIDRs []string `json:"allowed_cidrs,omitempty"`
	HeadersFile  string   `json:"headers_file,omitempty"`
	Image        string   `json:"image,omitempty"`
	Args         []string `json:"args,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, errors.New("cannot read Connector configuration")
	}
	if len(raw) > 64<<10 {
		return c, errors.New("Connector configuration is too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, errors.New("invalid Connector configuration")
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	u, err := url.Parse(c.GatewayURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") {
		return errors.New("gateway_url must be an HTTPS origin")
	}
	if !filepath.IsAbs(c.TokenFile) || !filepath.IsAbs(c.StateDir) || len(c.Targets) == 0 || len(c.Targets) > 32 {
		return errors.New("absolute token_file/state_dir and 1–32 targets are required")
	}
	seen := map[string]bool{}
	for _, t := range c.Targets {
		if !targetName.MatchString(t.Name) || seen[t.Name] {
			return errors.New("invalid or duplicate local target name")
		}
		seen[t.Name] = true
		switch t.Transport {
		case "http":
			u, err := url.Parse(t.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || t.Image != "" || len(t.Args) > 0 {
				return errors.New("HTTP targets require a fixed HTTP(S) URL without embedded credentials")
			}
			if t.HeadersFile != "" && !filepath.IsAbs(t.HeadersFile) {
				return errors.New("headers_file must be absolute")
			}
			if len(t.AllowedCIDRs) > 32 {
				return errors.New("too many allowed networks")
			}
			for _, cidr := range t.AllowedCIDRs {
				if _, err := netip.ParsePrefix(cidr); err != nil {
					return errors.New("invalid allowed CIDR")
				}
			}
		case "stdio":
			if !imageDigest.MatchString(t.Image) || t.URL != "" || t.HeadersFile != "" || len(t.AllowedCIDRs) > 0 || len(t.Args) > 64 {
				return errors.New("stdio targets require an immutable image digest and cannot access HTTP credentials or networks")
			}
			for _, arg := range t.Args {
				if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
					return errors.New("invalid local container argument")
				}
			}
		default:
			return errors.New("target transport must be http or stdio")
		}
	}
	return nil
}
func (t TargetConfig) Wire() connectorwire.Target {
	canonical := t
	canonical.AllowedCIDRs = append([]string(nil), t.AllowedCIDRs...)
	sort.Strings(canonical.AllowedCIDRs)
	b, _ := json.Marshal(struct {
		Profile string       `json:"profile"`
		Target  TargetConfig `json:"target"`
	}{"rillgate-connector-1", canonical})
	sum := sha256.Sum256(b)
	return connectorwire.Target{Name: t.Name, Transport: t.Transport, Fingerprint: hex.EncodeToString(sum[:])}
}
func (c Config) TargetsWire() []connectorwire.Target {
	out := make([]connectorwire.Target, 0, len(c.Targets))
	for _, t := range c.Targets {
		out = append(out, t.Wire())
	}
	return out
}

// Reject symlinks at every path component, including secret and journal parents.
func safePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path must be clean and absolute")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		s, err := os.Lstat(current)
		if err != nil {
			return errors.New("local path is unavailable")
		}
		if s.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlinks are not allowed for Connector state or secrets")
		}
	}
	return nil
}
func readSecret(path string) ([]byte, error) {
	if err := safePath(path); err != nil {
		return nil, err
	}
	f, err := openPrivate(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, errors.New("cannot open private secret file")
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || !ownedByCurrentUser(s) {
		return nil, errors.New("secret file must be owned by the Connector user with mode 0600")
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("invalid secret file size")
	}
	return data, nil
}
func loadToken(path string) (string, error) {
	b, err := readSecret(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 24 || len(token) > 4096 || strings.ContainsAny(token, " \r\n\t") {
		return "", fmt.Errorf("invalid Connector token")
	}
	return token, nil
}
