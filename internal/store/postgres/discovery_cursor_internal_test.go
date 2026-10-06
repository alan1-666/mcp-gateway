package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

func TestToolCursorRejectsMalformedEncodingAndBindings(t *testing.T) {
	actor := core.Actor{ID: "actor", WorkspaceID: "workspace", Role: core.RoleOperator, ClientID: "client", ClientKeyID: "key"}
	input := core.ToolSearchInput{Query: "status", ServerID: "server"}
	upper := time.Now().UTC()
	encoded, err := encodeToolCursor(actor, input, core.ToolScopeCatalog, upper, core.Tool{ID: "tool", CreatedAt: upper.Add(-time.Minute)}, 500)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for name, invalid := range map[string]string{"empty": "", "oversized": strings.Repeat("a", 2049), "bad_base64": "!!", "invalid_json": base64.RawURLEncoding.EncodeToString([]byte("{")), "trailing_json": base64.RawURLEncoding.EncodeToString(append(raw, []byte(" {}")...))} {
		t.Run(name, func(t *testing.T) {
			in := input
			in.Cursor = invalid
			if _, err := decodeToolCursor(actor, in, core.ToolScopeCatalog); !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("accepted %q: %v", invalid, err)
			}
		})
	}
	var original toolCursor
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*toolCursor){
		func(c *toolCursor) { c.AfterID = strings.Repeat("a", 129) }, func(c *toolCursor) { c.AfterID = "a\r" },
		func(c *toolCursor) { c.AfterTime = time.Time{} }, func(c *toolCursor) { c.Upper = time.Time{} },
		func(c *toolCursor) { c.AfterTime = time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC) }, func(c *toolCursor) { c.Upper = time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC) },
		func(c *toolCursor) { c.AfterTime = c.Upper.Add(time.Second) }, func(c *toolCursor) { c.Query = ""; c.AfterScore = 100 },
	} {
		c := original
		mutate(&c)
		body, _ := json.Marshal(c)
		in := input
		in.Query = c.Query
		in.Cursor = base64.RawURLEncoding.EncodeToString(body)
		if _, err := decodeToolCursor(actor, in, core.ToolScopeCatalog); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("accepted mutated cursor %+v: %v", c, err)
		}
	}
	in := input
	in.Cursor = encoded
	if got, err := decodeToolCursor(actor, in, core.ToolScopeCatalog); err != nil || got.AfterScore != 500 || got.ClientKeyID != "key" {
		t.Fatalf("round trip %+v %v", got, err)
	}
	hugeActor := actor
	hugeActor.WorkspaceID = strings.Repeat("a", 2048)
	if _, err := encodeToolCursor(hugeActor, input, core.ToolScopeCatalog, upper, core.Tool{ID: "tool", CreatedAt: upper}, 500); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("oversized identity accepted: %v", err)
	}
	if _, err := encodeToolCursor(actor, input, core.ToolScopeCatalog, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), core.Tool{}, 500); err == nil {
		t.Fatal("invalid JSON timestamp accepted")
	}
}

func TestRepositoryDiscoveryValidatesBeforeDatabaseAccess(t *testing.T) {
	repo := new(Repository)
	actor := core.Actor{ID: "a", WorkspaceID: "w", Role: core.RoleAdmin}
	for _, input := range []core.ToolSearchInput{{Limit: -1}, {Cursor: "bad!"}} {
		if _, err := repo.SearchTools(context.Background(), actor, input, core.ToolScopeCatalog); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("invalid request reached storage: %v", err)
		}
	}
}
