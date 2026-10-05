package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type policyReadRepository struct {
	Repository
	tool   Tool
	reads  int
	writes int
}

func (r *policyReadRepository) GetTool(_ context.Context, a Actor, id string) (Tool, error) {
	r.reads++
	if a.WorkspaceID != r.tool.WorkspaceID || id != r.tool.ID {
		return Tool{}, ErrNotFound
	}
	return r.tool, nil
}
func (r *policyReadRepository) UpdateToolResponsePolicy(context.Context, Actor, string, ResponsePolicyUpdateInput) (Tool, error) {
	r.writes++
	return r.tool, nil
}
func policyService() (*Service, *policyReadRepository, Actor) {
	actor := Actor{ID: "admin", WorkspaceID: "team", Role: RoleAdmin}
	repo := &policyReadRepository{tool: Tool{ID: "tool", WorkspaceID: "team", Version: 2, MCP: &MCPConfig{ServerID: "server"}, OutputSchema: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}}`)}}
	return NewService(repo), repo, actor
}
func TestResponsePolicyPreviewPreservesNumbersAndDoesNotWrite(t *testing.T) {
	svc, repo, actor := policyService()
	sample := json.RawMessage(`{ "isError": false, "content": [{"type":"text","text":"private original","_meta":{"private":true}}], "_meta":{"discard":true}, "structuredContent": {"id":9007199254740993123,"private":"excluded"} }`)
	got, err := svc.PreviewToolResponsePolicy(context.Background(), actor, "tool", ResponsePolicyPreviewInput{ExpectedVersion: 2, ResponsePolicy: &ResponsePolicy{Include: []string{"/id"}}, Sample: sample})
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(map[string]any{"isError": false, "content": []any{map[string]any{"type": "text", "text": "private original"}}, "structuredContent": map[string]any{"id": json.Number("9007199254740993123"), "private": "excluded"}})
	if got.ToolVersion != 2 || got.OriginalBytes != len(canonical) || got.ProjectedBytes != len(got.Result) || repo.reads != 1 || repo.writes != 0 {
		t.Fatalf("wrong preview %+v reads=%d writes=%d", got, repo.reads, repo.writes)
	}
	if !strings.Contains(string(got.Result), "9007199254740993123") || strings.Contains(string(got.Result), "private") || strings.Contains(string(got.Result), "_meta") {
		t.Fatalf("bad projected value %s", got.Result)
	}
}
func TestResponsePolicyPreviewValidation(t *testing.T) {
	svc, repo, actor := policyService()
	valid := ResponsePolicyPreviewInput{ExpectedVersion: 2, ResponsePolicy: &ResponsePolicy{}, Sample: json.RawMessage(`{"isError":false,"content":[],"structuredContent":{"id":1}}`)}
	for _, raw := range []string{"", `null`, `[]`, `{"isError":true,"content":[]}`, `{"content":[]}`, `{"isError":false}`, `{"isError":false,"content":null}`, `{"isError":false,"content":[{"type":"image","data":"abc"}]}`, `{"isError":false,"content":[{"type":"text","text":12}]}`, `{"isError":false,"content":[{"type":"text","text":"only text"}]}`, `{"isError":false,"content":[],"structuredContent":null}`, `{"isError":false,"content":[],"structuredContent":{"id":"wrong type"}}`} {
		in := valid
		in.Sample = json.RawMessage(raw)
		if _, err := svc.PreviewToolResponsePolicy(context.Background(), actor, "tool", in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %s err=%v", raw, err)
		}
	}
	in := valid
	in.Sample = json.RawMessage(`{"isError":false,"content":[{"type":"text","text":"` + strings.Repeat("a", MaxArgumentsBytes) + `"}]}`)
	if _, err := svc.PreviewToolResponsePolicy(context.Background(), actor, "tool", in); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized sample", err)
	}
	in = valid
	in.ResponsePolicy = &ResponsePolicy{Include: []string{"/missing"}}
	if _, err := svc.PreviewToolResponsePolicy(context.Background(), actor, "tool", in); !errors.Is(err, ErrInvalid) {
		t.Fatal("projection failure not invalid", err)
	}
	in = valid
	in.ExpectedVersion = 1
	if _, err := svc.PreviewToolResponsePolicy(context.Background(), actor, "tool", in); !errors.Is(err, ErrConflict) {
		t.Fatal("stale preview", err)
	}
	for _, in := range []ResponsePolicyPreviewInput{{ExpectedVersion: 0, ResponsePolicy: &ResponsePolicy{}, Sample: valid.Sample}, {ExpectedVersion: 2, Sample: valid.Sample}} {
		if _, err := svc.PreviewToolResponsePolicy(context.Background(), actor, "tool", in); !errors.Is(err, ErrInvalid) {
			t.Fatal("required fields", err)
		}
	}
	repo.tool.MCP = nil
	if _, err := svc.PreviewToolResponsePolicy(context.Background(), actor, "tool", valid); !errors.Is(err, ErrInvalid) {
		t.Fatal("HTTP tool accepted", err)
	}
}
func TestResponsePolicyAdministrativeAccess(t *testing.T) {
	svc, repo, actor := policyService()
	ctx := context.Background()
	preview := ResponsePolicyPreviewInput{ExpectedVersion: 2, ResponsePolicy: &ResponsePolicy{}, Sample: json.RawMessage(`{"isError":false,"content":[],"structuredContent":{"id":1}}`)}
	for _, role := range []Role{RoleOperator, RoleApprover, RoleViewer} {
		actor.Role = role
		if _, err := svc.PreviewToolResponsePolicy(ctx, actor, "tool", preview); !errors.Is(err, ErrForbidden) {
			t.Fatal("preview role", role, err)
		}
		if _, err := svc.UpdateToolResponsePolicy(ctx, actor, "tool", ResponsePolicyUpdateInput{ExpectedVersion: 2, ResponsePolicy: &ResponsePolicy{}}); !errors.Is(err, ErrForbidden) {
			t.Fatal("update role", role, err)
		}
	}
	if repo.reads != 0 || repo.writes != 0 {
		t.Fatal("unauthorized repository access")
	}
	actor.Role = RoleAdmin
	actor.WorkspaceID = "foreign"
	if _, err := svc.PreviewToolResponsePolicy(ctx, actor, "tool", preview); !errors.Is(err, ErrNotFound) {
		t.Fatal("workspace preview", err)
	}
	actor.ID = ""
	if _, err := svc.UpdateToolResponsePolicy(ctx, actor, "tool", ResponsePolicyUpdateInput{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("anonymous update", err)
	}
}
func TestResponsePolicyTextPreviewStripsProtocolMetadata(t *testing.T) {
	svc, repo, actor := policyService()
	repo.tool.OutputSchema = nil
	got, err := svc.PreviewToolResponsePolicy(context.Background(), actor, "tool", ResponsePolicyPreviewInput{ExpectedVersion: 2, ResponsePolicy: &ResponsePolicy{}, Sample: json.RawMessage(`{"isError":false,"content":[{"type":"text","text":"plain","_meta":{"private":"secret"}}],"_meta":{"private":"secret"}}`)})
	if err != nil || strings.Contains(string(got.Result), "secret") || !strings.Contains(string(got.Result), "plain") {
		t.Fatalf("text preview %s %v", got.Result, err)
	}
}
