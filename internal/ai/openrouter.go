package ai

import (
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/packages/respjson"
)

// openRouterProviderObject builds the value spliced into the request body as
// the "provider" field. Returns nil when no routing preferences are set, so
// the caller can omit the field entirely. RawJSON, when present, wins over
// the structured fields — power users want full control.
func openRouterProviderObject(cfg ProviderConfig) (any, error) {
	if cfg.RawJSON != "" {
		var v any
		if err := json.Unmarshal([]byte(cfg.RawJSON), &v); err != nil {
			return nil, fmt.Errorf("openrouter-provider-json: %w", err)
		}
		return v, nil
	}
	obj := map[string]any{}
	if cfg.DenyDataCollection {
		obj["data_collection"] = "deny"
	}
	if cfg.NoFallbacks {
		obj["allow_fallbacks"] = false
	}
	if len(obj) == 0 {
		return nil, nil
	}
	return obj, nil
}

// extractCost reads the per-call USD cost OpenRouter exposes on the response
// usage block. Returns 0 when the field is absent or unparseable — callers
// treat zero as "unknown" and suppress UI.
func extractCost(extras map[string]respjson.Field) float64 {
	f, ok := extras["cost"]
	if !ok {
		return 0
	}
	raw := f.Raw()
	if raw == "" || raw == "null" {
		return 0
	}
	var v float64
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return 0
	}
	return v
}
