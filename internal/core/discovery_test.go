package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeToolSearchBounds(t *testing.T) {
	for _, tc := range []struct {
		scope ToolVisibilityScope
		limit int
	}{{ToolScopeRegistry, 50}, {ToolScopeCatalog, 25}} {
		input, err := NormalizeToolSearch(ToolSearchInput{Query: "  中文订单  "}, tc.scope)
		if err != nil || input.Query != "中文订单" || input.Limit != tc.limit {
			t.Fatalf("normalize %+v %v", input, err)
		}
	}
	for _, input := range []ToolSearchInput{
		{Query: strings.Repeat("x", 201)},
		{Query: strings.Repeat("中", 67)},
		{Query: string([]byte{0xff})},
		{Query: "a\x00b"},
		{ServerID: strings.Repeat("x", 129)},
		{ServerID: "abc\x00"},
		{ServerID: "a\rb"},
		{ServerID: string([]byte{0xff})},
		{Limit: -1},
		{Limit: 51},
		{Cursor: strings.Repeat("a", 2049)},
	} {
		if _, err := NormalizeToolSearch(input, ToolScopeCatalog); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid search accepted: %+v %v", input, err)
		}
	}
	if _, err := NormalizeToolSearch(ToolSearchInput{Query: strings.Repeat("中", 66), Limit: 50}, ToolScopeCatalog); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeToolSearch(ToolSearchInput{}, "unknown"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown scope accepted: %v", err)
	}
}

func TestToolSummaryDescriptionTruncation(t *testing.T) {
	for _, text := range []string{"", "Read 中文订单", strings.Repeat("a", 512), strings.Repeat("中", 170) + "ab"} {
		if got := summarizeToolDescription(text); got != text {
			t.Fatalf("short description changed: got %q want %q", got, text)
		}
	}
	for _, text := range []string{strings.Repeat("a", 513), strings.Repeat("中", 200), strings.Repeat("x", 508) + "中尾", strings.Repeat("🐈", 150)} {
		got := summarizeToolDescription(text)
		if len(got) > 512 || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
			t.Fatalf("invalid truncated description (%d bytes): %q", len(got), got)
		}
		prefix := strings.TrimSuffix(got, "…")
		if !strings.HasPrefix(text, prefix) {
			t.Fatal("summary no longer starts with the original description")
		}
		nextRune, width := utf8.DecodeRuneInString(text[len(prefix):])
		if nextRune == utf8.RuneError && width == 0 {
			t.Fatal("unexpected empty remainder")
		}
		if len(prefix)+width+len("…") <= 512 {
			t.Fatal("summary discarded a complete rune that fit the byte budget")
		}
	}
}

func TestMaximumDiscoveryPageFitsClientResponseLimit(t *testing.T) {
	for name, description := range map[string]string{
		"escaped_ascii": strings.Repeat("<>&\x01", 1000),
		"mixed_chinese": strings.Repeat("<>&\x01中文", 400),
		"chinese":       strings.Repeat("中", 1333) + "x",
	} {
		t.Run(name, func(t *testing.T) {
			if len(description) != 4000 {
				t.Fatal("fixture must use the maximum legal description length")
			}
			repo := &searchRepository{page: ToolPage{Items: make([]Tool, 50), NextCursor: strings.Repeat("a", 2048), Total: 999999999}}
			for i := range repo.page.Items {
				repo.page.Items[i] = Tool{ID: NewID(), Name: strings.Repeat("n", 58) + fmt.Sprintf("%06d", i), Description: description, Risk: RiskWrite, Version: 2147483647}
			}
			page, err := NewService(repo).DiscoverTools(context.Background(), Actor{ID: "actor", WorkspaceID: "workspace", Role: RoleOperator}, ToolSearchInput{Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 50 {
				t.Fatalf("page lost items: %d", len(page.Items))
			}
			for _, item := range page.Items {
				if len(item.Description) > 512 || !utf8.ValidString(item.Description) || !strings.HasSuffix(item.Description, "…") {
					t.Fatal("unbounded summary description")
				}
			}
			encoded, err := json.Marshal(page)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) >= 256<<10 {
				t.Fatalf("discovery page exceeds client response limit: %d bytes", len(encoded))
			}
			if repo.page.Items[0].Description != description {
				t.Fatal("summary mutated the complete tool description")
			}
		})
	}
}

type searchRepository struct {
	Repository
	input ToolSearchInput
	scope ToolVisibilityScope
	page  ToolPage
	err   error
}

func (r *searchRepository) SearchTools(_ context.Context, _ Actor, input ToolSearchInput, scope ToolVisibilityScope) (ToolPage, error) {
	r.input = input
	r.scope = scope
	return r.page, r.err
}

func TestServiceControlsSearchScopeAndSummary(t *testing.T) {
	repo := &searchRepository{page: ToolPage{Items: []Tool{{ID: "tool", Name: "orders", Description: "Order status", Risk: RiskRead, Version: 1, WorkspaceID: "private-workspace", HTTP: HTTPConfig{URL: "https://internal.example", CredentialRef: "SECRET_REF"}, InputSchema: json.RawMessage(`{"type":"object"}`)}}, NextCursor: "position", Total: 70}}
	service := NewService(repo)
	actor := Actor{ID: "admin", WorkspaceID: "workspace", Role: RoleAdmin}
	page, err := service.DiscoverTools(context.Background(), actor, ToolSearchInput{Query: " orders "})
	if err != nil {
		t.Fatal(err)
	}
	if repo.scope != ToolScopeCatalog || repo.input.Limit != 25 || repo.input.Query != "orders" || page.Total != 70 || page.NextCursor != "position" {
		t.Fatalf("discovery scope/page: %+v %+v", repo, page)
	}
	data, err := json.Marshal(page.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 || fields["id"] != "tool" {
		t.Fatalf("summary fields leaked: %s", data)
	}
	if _, err := service.SearchTools(context.Background(), actor, ToolSearchInput{}); err != nil {
		t.Fatal(err)
	}
	if repo.scope != ToolScopeRegistry || repo.input.Limit != 50 {
		t.Fatalf("registry scope/default %+v", repo)
	}
	if _, err := service.DiscoverTools(context.Background(), Actor{}, ToolSearchInput{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthenticated discovery: %v", err)
	}
}

func TestDiscoverySummaryCarriesBoundedSearchExplanation(t *testing.T) {
	repo := &searchRepository{page: ToolPage{Items: []Tool{{ID: "a", Name: "status", MCP: &MCPConfig{ServerID: "server", ToolName: "private", SchemaHash: "private"}}}, Matches: map[string]ToolMatch{"a": {Score: 500, Reason: "exact_name"}}}}
	actor := Actor{ID: "a", WorkspaceID: "w", Role: RoleAdmin}
	page, err := NewService(repo).DiscoverTools(context.Background(), actor, ToolSearchInput{Query: "status", ServerID: " server "})
	if err != nil || repo.input.ServerID != "server" || page.Items[0].ServerID != "server" || page.Items[0].Match == nil || page.Items[0].Match.Score != 500 {
		t.Fatalf("summary %+v %v", page, err)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "private") {
		t.Fatal("upstream configuration leaked")
	}
	page, err = NewService(repo).DiscoverTools(context.Background(), actor, ToolSearchInput{})
	if err != nil || page.Items[0].Match != nil {
		t.Fatalf("empty query must not imply relevance %+v %v", page, err)
	}
}

func TestDiscoveryServiceRejectsInvalidRequestsAndPropagatesStoreErrors(t *testing.T) {
	repo := &searchRepository{err: errors.New("storage unavailable")}
	svc := NewService(repo)
	actor := Actor{ID: "admin", WorkspaceID: "workspace", Role: RoleAdmin}
	if _, err := svc.SearchTools(context.Background(), Actor{}, ToolSearchInput{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("registry auth %v", err)
	}
	for _, scope := range []string{"registry", "catalog"} {
		t.Run(scope, func(t *testing.T) {
			if scope == "registry" {
				if _, err := svc.SearchTools(context.Background(), actor, ToolSearchInput{Limit: -1}); !errors.Is(err, ErrInvalid) {
					t.Fatalf("invalid search %v", err)
				}
				if _, err := svc.SearchTools(context.Background(), actor, ToolSearchInput{}); !errors.Is(err, repo.err) {
					t.Fatalf("store error %v", err)
				}
			} else {
				if _, err := svc.DiscoverTools(context.Background(), actor, ToolSearchInput{Limit: -1}); !errors.Is(err, ErrInvalid) {
					t.Fatalf("invalid search %v", err)
				}
				if _, err := svc.DiscoverTools(context.Background(), actor, ToolSearchInput{}); !errors.Is(err, repo.err) {
					t.Fatalf("store error %v", err)
				}
			}
		})
	}
}
