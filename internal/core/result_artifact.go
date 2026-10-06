package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ResultArtifactPolicy is an explicit, versioned opt-in. MaxBytes bounds the
// complete projected MCP envelope; only structuredContent is retained separately.
type ResultArtifactPolicy struct {
	MaxBytes   int `json:"max_bytes"`
	TTLSeconds int `json:"ttl_seconds"`
}

func NormalizeResultArtifactPolicy(p ResultArtifactPolicy, inline int) (ResultArtifactPolicy, error) {
	if p.MaxBytes == 0 {
		p.MaxBytes = 512 << 10
	}
	if p.TTLSeconds == 0 {
		p.TTLSeconds = 3600
	}
	if p.MaxBytes < inline || p.MaxBytes > MaxResultBytes || p.TTLSeconds < 60 || p.TTLSeconds > 86400 {
		return p, fmt.Errorf("%w: artifact max_bytes must be between inline max_bytes and 1048576; ttl_seconds must be 60–86400", ErrInvalid)
	}
	return p, nil
}

type ResultReference struct {
	OperationID string    `json:"operation_id"`
	Bytes       int       `json:"bytes"`
	SHA256      string    `json:"sha256"`
	ExpiresAt   time.Time `json:"expires_at"`
	Format      string    `json:"format"`
}
type ResultReadInput struct {
	OperationID string `json:"operation_id"`
	Cursor      string `json:"cursor,omitempty"`
	LimitBytes  int    `json:"limit_bytes,omitempty"`
}
type ResultPage struct {
	ResultReference
	Offset     int    `json:"offset"`
	Chunk      string `json:"chunk"`
	NextCursor string `json:"next_cursor,omitempty"`
}
type resultReader interface {
	ReadResult(context.Context, Actor, ResultReadInput) (ResultPage, error)
}

func (s *Service) ReadResult(ctx context.Context, actor Actor, in ResultReadInput) (ResultPage, error) {
	if err := authorize(actor); err != nil {
		return ResultPage{}, err
	}
	if in.OperationID == "" || len(in.OperationID) > 128 || len(in.Cursor) > 512 || (in.LimitBytes != 0 && (in.LimitBytes < 1024 || in.LimitBytes > 16384)) {
		return ResultPage{}, fmt.Errorf("%w: operation_id, cursor or limit_bytes out of bounds", ErrInvalid)
	}
	if in.LimitBytes == 0 {
		in.LimitBytes = 8192
	}
	repo, ok := s.repo.(resultReader)
	if !ok {
		return ResultPage{}, ErrNotFound
	}
	return repo.ReadResult(ctx, actor, in)
}

// ResultChunk uses byte offsets in immutable UTF-8 JSON. A cursor is bound to its
// operation and digest; possession never grants access. Concatenate chunks in
// order and parse once to retain exact JSON number representations.
func ResultChunk(ref ResultReference, raw []byte, in ResultReadInput) (ResultPage, error) {
	invalid := func() (ResultPage, error) {
		return ResultPage{}, fmt.Errorf("%w: invalid result cursor or payload", ErrInvalid)
	}
	if in.LimitBytes < 1024 || in.LimitBytes > 16384 || len(raw) != ref.Bytes || !utf8.Valid(raw) || !json.Valid(raw) || ref.OperationID != in.OperationID {
		return invalid()
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != ref.SHA256 {
		return invalid()
	}
	offset := 0
	if in.Cursor != "" {
		b, err := base64.RawURLEncoding.Strict().DecodeString(in.Cursor)
		if err != nil || len(b) > 384 {
			return invalid()
		}
		parts := strings.Split(string(b), ":")
		if len(parts) != 3 || parts[0] != ref.OperationID || parts[1] != ref.SHA256 {
			return invalid()
		}
		offset, err = strconv.Atoi(parts[2])
		if err != nil || strconv.Itoa(offset) != parts[2] || offset <= 0 || offset >= len(raw) || !utf8.RuneStart(raw[offset]) {
			return invalid()
		}
	}
	end := min(len(raw), offset+in.LimitBytes)
	for end < len(raw) && !utf8.RuneStart(raw[end]) {
		end--
	}
	page := ResultPage{ResultReference: ref, Offset: offset, Chunk: string(raw[offset:end])}
	if end < len(raw) {
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(ref.OperationID + ":" + ref.SHA256 + ":" + strconv.Itoa(end)))
	}
	return page, nil
}
