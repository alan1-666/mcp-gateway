package connectoragent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigPolicyAndFingerprint(t *testing.T) {
	target := TargetConfig{Name: "private", Transport: "http", URL: "http://127.0.0.1:3000/mcp", AllowedCIDRs: []string{"127.0.0.1/32", "10.0.0.0/8"}}
	c := testConfig(t, "https://gateway.example", target)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"http://gateway.example", "https://user:pass@gateway.example", "https://gateway.example/?token=secret", "https://gateway.example/#fragment", "https://gateway.example/prefix", "https://gateway.example/?"} {
		copy := c
		copy.GatewayURL = bad
		if copy.Validate() == nil {
			t.Errorf("unsafe cloud origin %q", bad)
		}
	}
	copy := target
	copy.AllowedCIDRs = []string{"10.0.0.0/8", "127.0.0.1/32"}
	if copy.Wire() != target.Wire() {
		t.Fatal("CIDR ordering caused drift")
	}
	copy.URL = "http://127.0.0.1:3001/mcp"
	if copy.Wire() == target.Wire() {
		t.Fatal("target drift did not change fingerprint")
	}
	copy = target
	copy.HeadersFile = "/private/credentials"
	if copy.Wire() == target.Wire() {
		t.Fatal("credential reference missing from fingerprint")
	}
	bad := stdioTarget()
	bad.Image = "fixture:latest"
	c.Targets = []TargetConfig{bad}
	if c.Validate() == nil {
		t.Fatal("mutable image accepted")
	}
	bad = stdioTarget()
	bad.URL = "http://127.0.0.1"
	c.Targets = []TargetConfig{bad}
	if c.Validate() == nil {
		t.Fatal("mixed target transports accepted")
	}
	args := strings.Join(podmanArgs(stdioTarget(), "rillgate-test"), " ")
	for _, flag := range []string{"--remote=false", "--pull=never", "--log-driver=none", "--http-proxy=false", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=64", "--memory=256m", "--cpus=1", "--user=65532:65532", "nosuid,nodev,noexec"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("missing isolation %s", flag)
		}
	}
}
func TestJournalLocksDurabilityCorruptionAndSecrets(t *testing.T) {
	c := testConfig(t, "https://gateway.example", TargetConfig{Name: "private", Transport: "http", URL: "http://127.0.0.1/mcp"})
	j, err := openJournal(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := openJournal(c.StateDir); err == nil {
		other.Close()
		t.Fatal("concurrent journal lock accepted")
	}
	entry := journalEntry{ID: "once", Deadline: time.Now().Add(time.Second)}
	if err := j.Record(entry); err != nil {
		t.Fatal(err)
	}
	j.Close()
	j, err = openJournal(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if j.Record(entry) == nil {
		t.Fatal("duplicate journal record")
	}
	j.Close()
	if err := os.WriteFile(filepath.Join(c.StateDir, "journal.jsonl"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if j, err := openJournal(c.StateDir); err == nil {
		j.Close()
		t.Fatal("corrupt journal accepted")
	}
	if err := os.Chmod(c.TokenFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToken(c.TokenFile); err == nil {
		t.Fatal("world-readable token accepted")
	}
	if err := os.Chmod(c.TokenFile, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(c.TokenFile), "secret-link")
	if err := os.Symlink(c.TokenFile, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToken(link); err == nil {
		t.Fatal("symlink secret accepted")
	}
}
func TestJournalCapacityAndSafeRetention(t *testing.T) {
	dir := filepath.Join(privateTemp(t), "state")
	j, err := openJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for i := 0; i < journalLimit; i++ {
		id := strings.Repeat("x", i%10) + time.Unix(int64(i), 0).String()
		j.entries[id] = journalEntry{ID: id, Deadline: time.Now()}
	}
	if j.Record(journalEntry{ID: "new", Deadline: time.Now()}) == nil {
		t.Fatal("journal capacity ignored")
	}
	for id, entry := range j.entries {
		entry.Deadline = time.Now().Add(-25 * time.Hour)
		j.entries[id] = entry
	}
	if err := j.Record(journalEntry{ID: "new", Deadline: time.Now().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if len(j.entries) != 1 {
		t.Fatal("expired entries were not compacted")
	}
}

func TestPodmanRequiresEnforceableResourceControllers(t *testing.T) {
	valid := `{"host":{"security":{"rootless":true},"cgroupVersion":"v2","cgroupControllers":["cpu","cpuset","io","memory","pids"]},"store":{"ignored":"metadata"}}`
	if !podmanResourceSupport(valid) {
		t.Fatal("supported rootless host rejected")
	}
	for _, profile := range []string{strings.Replace(valid, `"v2"`, `"v1"`, 1), strings.Replace(valid, `true`, `false`, 1), strings.Replace(valid, `"cpu",`, ``, 1), strings.Replace(valid, `"memory",`, ``, 1), strings.Replace(valid, `,"pids"`, ``, 1), `{}`, `broken`, valid + `{}`} {
		if podmanResourceSupport(profile) {
			t.Errorf("unenforceable resource profile accepted: %s", profile)
		}
	}
	oversized := limitedOutput{limit: 64 << 10}
	if _, err := oversized.Write(make([]byte, (64<<10)+1)); err == nil {
		t.Fatal("Podman info output limit ignored")
	}
}
