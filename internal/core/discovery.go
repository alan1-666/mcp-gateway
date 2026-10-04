package core

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

type ToolSearchInput struct {
	Query  string `json:"query,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// ToolVisibilityScope is chosen by the service, never by an untrusted cursor.
type ToolVisibilityScope string

const (
	ToolScopeRegistry ToolVisibilityScope = "registry"
	ToolScopeCatalog  ToolVisibilityScope = "catalog"
)

type ToolPage struct {
	Items      []Tool `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	Total      int64  `json:"total"`
}
type ToolSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        Risk   `json:"risk"`
	Version     int    `json:"version"`
}
type ToolDiscoveryPage struct {
	Items      []ToolSummary `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
	Total      int64         `json:"total"`
}

const toolSummaryDescriptionBytes = 512

// Tool descriptions are discovery hints. Keep summaries bounded even when JSON
// escaping expands a byte to six characters; the complete text remains on Tool.
func summarizeToolDescription(description string) string {
	if len(description) <= toolSummaryDescriptionBytes {
		return description
	}
	const suffix = "…"
	end := toolSummaryDescriptionBytes - len(suffix)
	for end > 0 && !utf8.ValidString(description[:end]) {
		end--
	}
	return description[:end] + suffix
}

// NormalizeToolSearch uses zero only as the internal "omitted" limit. Protocol
// handlers must distinguish an omitted limit from an explicitly supplied zero.
func NormalizeToolSearch(input ToolSearchInput, scope ToolVisibilityScope) (ToolSearchInput, error) {
	if scope != ToolScopeRegistry && scope != ToolScopeCatalog {
		return input, fmt.Errorf("%w: invalid tool visibility scope", ErrInvalid)
	}
	input.Query = strings.TrimSpace(input.Query)
	if len(input.Query) > 200 || !utf8.ValidString(input.Query) || strings.ContainsRune(input.Query, 0) {
		return input, fmt.Errorf("%w: query must contain at most 200 UTF-8 bytes without NUL", ErrInvalid)
	}
	if len(input.Cursor) > 2048 || !utf8.ValidString(input.Cursor) {
		return input, fmt.Errorf("%w: invalid tool cursor", ErrInvalid)
	}
	if input.Limit == 0 {
		input.Limit = 50
		if scope == ToolScopeCatalog {
			input.Limit = 25
		}
	}
	if input.Limit < 1 || input.Limit > 50 {
		return input, fmt.Errorf("%w: limit must be between 1 and 50", ErrInvalid)
	}
	return input, nil
}

func (s *Service) SearchTools(ctx context.Context, actor Actor, input ToolSearchInput) (ToolPage, error) {
	if err := authorize(actor); err != nil {
		return ToolPage{}, err
	}
	input, err := NormalizeToolSearch(input, ToolScopeRegistry)
	if err != nil {
		return ToolPage{}, err
	}
	return s.repo.SearchTools(ctx, actor, input, ToolScopeRegistry)
}

func (s *Service) DiscoverTools(ctx context.Context, actor Actor, input ToolSearchInput) (ToolDiscoveryPage, error) {
	if err := authorize(actor); err != nil {
		return ToolDiscoveryPage{}, err
	}
	input, err := NormalizeToolSearch(input, ToolScopeCatalog)
	if err != nil {
		return ToolDiscoveryPage{}, err
	}
	page, err := s.repo.SearchTools(ctx, actor, input, ToolScopeCatalog)
	if err != nil {
		return ToolDiscoveryPage{}, err
	}
	result := ToolDiscoveryPage{Items: make([]ToolSummary, 0, len(page.Items)), NextCursor: page.NextCursor, Total: page.Total}
	for _, tool := range page.Items {
		result.Items = append(result.Items, ToolSummary{ID: tool.ID, Name: tool.Name, Description: summarizeToolDescription(tool.Description), Risk: tool.Risk, Version: tool.Version})
	}
	return result, nil
}
