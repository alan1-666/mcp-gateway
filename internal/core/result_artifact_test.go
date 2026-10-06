package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestResultArtifactPolicyBounds(t *testing.T) {
	p, err := NormalizeResultArtifactPolicy(ResultArtifactPolicy{}, 1024)
	if err != nil || p.MaxBytes != 512<<10 || p.TTLSeconds != 3600 {
		t.Fatal(p, err)
	}
	for _, p := range []ResultArtifactPolicy{{MaxBytes: 1023}, {MaxBytes: MaxResultBytes + 1}, {TTLSeconds: 59}, {TTLSeconds: 86401}} {
		if _, err := NormalizeResultArtifactPolicy(p, 1024); !errors.Is(err, ErrInvalid) {
			t.Fatal(p, err)
		}
	}
	for _, ttl := range []int{60, 86400} {
		if _, err := NormalizeResponsePolicy(&ResponsePolicy{MaxBytes: 1024, Artifact: &ResultArtifactPolicy{MaxBytes: 1024, TTLSeconds: ttl}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NormalizeResponsePolicy(&ResponsePolicy{Artifact: &ResultArtifactPolicy{TTLSeconds: 1}}); err == nil {
		t.Fatal("invalid artifact policy accepted")
	}
}

func TestResultChunksExactJSONUnicodeAndCursorBinding(t *testing.T) {
	raw := []byte(`{"id":9007199254740993123456789,"description":"` + strings.Repeat("中文🙂", 1100) + `"}`)
	sum := sha256.Sum256(raw)
	ref := ResultReference{OperationID: "op-1", Bytes: len(raw), SHA256: hex.EncodeToString(sum[:])}
	input := ResultReadInput{OperationID: "op-1", LimitBytes: 1024}
	var all strings.Builder
	for {
		p, err := ResultChunk(ref, raw, input)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(p.Chunk) || len(p.Chunk) > 1024 {
			t.Fatal("invalid boundary")
		}
		all.WriteString(p.Chunk)
		input.Cursor = p.NextCursor
		if input.Cursor == "" {
			break
		}
	}
	if all.String() != string(raw) {
		t.Fatal("reassembly changed exact bytes")
	}
	decoded, err := DecodeResult(json.RawMessage(all.String()))
	if err != nil || decoded.(map[string]any)["id"] != json.Number("9007199254740993123456789") {
		t.Fatal("precision lost", err)
	}
	for _, cursor := range []string{"!", base64.RawURLEncoding.EncodeToString([]byte("bad")), base64.RawURLEncoding.EncodeToString([]byte("other:" + ref.SHA256 + ":1024")), base64.RawURLEncoding.EncodeToString([]byte("op-1:bad:1024"))} {
		input.Cursor = cursor
		if _, err := ResultChunk(ref, raw, input); err == nil {
			t.Fatal("invalid cursor accepted", cursor)
		}
	}
	for _, offset := range []string{"0", "-1", "01", "9999999999999999999999999", "50000", "not-a-number"} {
		input.Cursor = base64.RawURLEncoding.EncodeToString([]byte("op-1:" + ref.SHA256 + ":" + offset))
		if _, err := ResultChunk(ref, raw, input); err == nil {
			t.Fatal(offset)
		}
	}
	for i, b := range raw {
		if b >= 128 && !utf8.RuneStart(b) {
			input.Cursor = base64.RawURLEncoding.EncodeToString([]byte("op-1:" + ref.SHA256 + ":" + strconv.Itoa(i)))
			if _, err := ResultChunk(ref, raw, input); err == nil {
				t.Fatal("split rune")
			}
			break
		}
	}
	input.Cursor = ""
	input.LimitBytes = 1
	if _, err := ResultChunk(ref, raw, input); err == nil {
		t.Fatal("unbounded limit")
	}
	input.LimitBytes = 1024
	bad := ref
	bad.Bytes++
	if _, err := ResultChunk(bad, raw, input); err == nil {
		t.Fatal("bad length")
	}
	bad = ref
	bad.SHA256 = "wrong"
	if _, err := ResultChunk(bad, raw, input); err == nil {
		t.Fatal("corrupt digest")
	}
}

type resultReadRepo struct {
	Repository
	calls int
}

func (r *resultReadRepo) ReadResult(_ context.Context, _ Actor, in ResultReadInput) (ResultPage, error) {
	r.calls++
	return ResultPage{Offset: in.LimitBytes}, nil
}
func TestResultReadAuthorizationAndValidation(t *testing.T) {
	repo := &resultReadRepo{}
	svc := NewService(repo)
	a := Actor{ID: "me", WorkspaceID: "w", Role: RoleOperator}
	ctx := context.Background()
	if _, err := svc.ReadResult(ctx, Actor{}, ResultReadInput{OperationID: "op"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	for _, in := range []ResultReadInput{{}, {OperationID: strings.Repeat("x", 129)}, {OperationID: "op", Cursor: strings.Repeat("x", 513)}, {OperationID: "op", LimitBytes: 1}, {OperationID: "op", LimitBytes: 16385}} {
		if _, err := svc.ReadResult(ctx, a, in); !errors.Is(err, ErrInvalid) {
			t.Fatal(in, err)
		}
	}
	p, err := svc.ReadResult(ctx, a, ResultReadInput{OperationID: "op"})
	if err != nil || p.Offset != 8192 || repo.calls != 1 {
		t.Fatal(p, err)
	}
	if _, err := NewService(unreachableRepository{}).ReadResult(ctx, a, ResultReadInput{OperationID: "op"}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestArtifactOptInRetainsBoundariesAndRedacts(t *testing.T) {
	raw := json.RawMessage(`{"isError":false,"content":[{"type":"text","text":"must-not-retain"}],"structuredContent":{"id":9007199254740993,"body":"` + strings.Repeat("x", 2000) + `","password":"private-secret","excluded":"unselected"}}`)
	if _, err := ApplyMCPResponsePolicy(raw, &ResponsePolicy{MaxBytes: 1024}); err == nil {
		t.Fatal("default inline bound bypassed")
	}
	p := &ResponsePolicy{MaxBytes: 1024, Include: []string{"/body", "/id", "/password"}, Artifact: &ResultArtifactPolicy{MaxBytes: 8192}}
	out, err := ApplyMCPResponsePolicy(raw, p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "private-secret") || strings.Contains(string(out), "unselected") || strings.Contains(string(out), "must-not-retain") || !strings.Contains(string(out), "9007199254740993") {
		t.Fatal("projection/filter/precision failed")
	}
	p.Artifact.MaxBytes = 1024
	if _, err := ApplyMCPResponsePolicy(raw, p); err == nil {
		t.Fatal("artifact bound bypassed")
	}
	p.Include = nil
	if _, err := ApplyMCPResponsePolicy(json.RawMessage(`{"isError":false,"content":[]}`), p); err == nil {
		t.Fatal("unstructured artifact accepted")
	}
}
