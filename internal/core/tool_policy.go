package core

import (
	"context"
	"encoding/json"
	"fmt"
)

// ResponsePolicyUpdateInput uses optimistic concurrency, including no-op updates.
type ResponsePolicyUpdateInput struct {
	ExpectedVersion int             `json:"expected_version"`
	ResponsePolicy  *ResponsePolicy `json:"response_policy"`
}
type ResponsePolicyPreviewInput struct {
	ExpectedVersion int             `json:"expected_version"`
	ResponsePolicy  *ResponsePolicy `json:"response_policy"`
	Sample          json.RawMessage `json:"sample"`
}
type ResponsePolicyPreview struct {
	ToolVersion    int             `json:"tool_version"`
	OriginalBytes  int             `json:"original_bytes"`
	ProjectedBytes int             `json:"projected_bytes"`
	Result         json.RawMessage `json:"result"`
}

func requiredResponsePolicy(version int, policy *ResponsePolicy) (*ResponsePolicy, error) {
	if version < 1 || policy == nil {
		return nil, fmt.Errorf("%w: a positive expected_version and non-null response_policy are required", ErrInvalid)
	}
	return NormalizeResponsePolicy(policy)
}
func (s *Service) UpdateToolResponsePolicy(ctx context.Context, actor Actor, id string, in ResponsePolicyUpdateInput) (Tool, error) {
	if err := authorize(actor, RoleAdmin); err != nil {
		return Tool{}, err
	}
	policy, err := requiredResponsePolicy(in.ExpectedVersion, in.ResponsePolicy)
	if err != nil {
		return Tool{}, err
	}
	in.ResponsePolicy = policy
	return s.repo.UpdateToolResponsePolicy(ctx, actor, id, in)
}

// PreviewToolResponsePolicy reads only the current tool contract. Sample data is
// never passed to the repository, upstream servers, operation ledger or audit log.
func (s *Service) PreviewToolResponsePolicy(ctx context.Context, actor Actor, id string, in ResponsePolicyPreviewInput) (ResponsePolicyPreview, error) {
	if err := authorize(actor, RoleAdmin); err != nil {
		return ResponsePolicyPreview{}, err
	}
	policy, err := requiredResponsePolicy(in.ExpectedVersion, in.ResponsePolicy)
	if err != nil {
		return ResponsePolicyPreview{}, err
	}
	tool, err := s.repo.GetTool(ctx, actor, id)
	if err != nil {
		return ResponsePolicyPreview{}, err
	}
	if tool.MCP == nil {
		return ResponsePolicyPreview{}, fmt.Errorf("%w: response policies require an MCP tool", ErrInvalid)
	}
	if tool.Version != in.ExpectedVersion {
		return ResponsePolicyPreview{}, fmt.Errorf("%w: tool version changed; reload before previewing", ErrConflict)
	}
	sample, originalBytes, err := validateResponseSample(in.Sample, tool.OutputSchema)
	if err != nil {
		return ResponsePolicyPreview{}, err
	}
	projected, err := ApplyMCPResponsePolicy(sample, policy)
	if err != nil {
		return ResponsePolicyPreview{}, fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	return ResponsePolicyPreview{ToolVersion: tool.Version, OriginalBytes: originalBytes, ProjectedBytes: len(projected), Result: projected}, nil
}
func validateResponseSample(raw, outputSchema json.RawMessage) (json.RawMessage, int, error) {
	invalid := func(message string) (json.RawMessage, int, error) {
		return nil, 0, fmt.Errorf("%w: %s", ErrInvalid, message)
	}
	if len(raw) == 0 || len(raw) > MaxArgumentsBytes {
		return invalid("sample must be a bounded MCP result object")
	}
	value, err := DecodeResult(raw)
	if err != nil {
		return invalid("sample contains malformed or unsupported JSON")
	}
	envelope, ok := value.(map[string]any)
	if !ok {
		return invalid("sample must be an MCP result object")
	}
	if flag, ok := envelope["isError"].(bool); !ok || flag {
		return invalid("sample must explicitly set isError to false")
	}
	content, ok := envelope["content"].([]any)
	if !ok {
		return invalid("sample content must be an array of text blocks")
	}
	supportedContent := make([]any, 0, len(content))
	for _, rawBlock := range content {
		block, ok := rawBlock.(map[string]any)
		if !ok || block["type"] != "text" {
			return invalid("sample supports text content blocks only")
		}
		if _, ok := block["text"].(string); !ok {
			return invalid("sample text block must contain text")
		}
		supportedContent = append(supportedContent, map[string]any{"type": "text", "text": block["text"]})
	}
	structured, hasStructured := envelope["structuredContent"]
	if hasStructured {
		if _, ok := structured.(map[string]any); !ok {
			return invalid("sample structuredContent must be an object")
		}
	}
	if len(outputSchema) > 0 {
		if !hasStructured {
			return invalid("sample requires structuredContent for the tool output schema")
		}
		schema, err := CompileSchema(outputSchema, false)
		if err != nil || schema.Validate(structured) != nil {
			return invalid("sample structuredContent does not match the tool output schema")
		}
	}
	// Count the supported envelope passed to Apply, matching live adapter output.
	// Ignored metadata/annotations cannot inflate the reported reduction.
	// json.Number preserves large identifiers through deterministic encoding.
	supported := map[string]any{"isError": false, "content": supportedContent}
	if hasStructured {
		supported["structuredContent"] = structured
	}
	projectedInput, err := json.Marshal(supported)
	if err != nil {
		return invalid("sample could not be normalized")
	}
	return projectedInput, len(projectedInput), nil
}
