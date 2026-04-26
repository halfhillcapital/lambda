package agent

import (
	"encoding/json"
	"strings"

	"github.com/openai/openai-go/packages/respjson"
)

// reasoningFieldKeys names the non-spec JSON fields that OpenAI-compatible
// servers use to expose chain-of-thought out-of-band: vLLM/SGLang/DeepSeek/
// recent LM Studio populate "reasoning_content"; Ollama uses "thinking".
// Reading these directly avoids any per-model inline-tag parsing.
var reasoningFieldKeys = []string{"reasoning_content", "thinking"}

// extractReasoning concatenates any reasoning text the server attached to a
// streaming delta or message via the well-known extra fields. Returns "" when
// the server doesn't expose reasoning out-of-band.
func extractReasoning(extras map[string]respjson.Field) string {
	if len(extras) == 0 {
		return ""
	}
	var b strings.Builder
	for _, key := range reasoningFieldKeys {
		f, ok := extras[key]
		if !ok {
			continue
		}
		raw := f.Raw()
		if raw == "" || raw == "null" {
			continue
		}
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			b.WriteString(s)
		}
	}
	return b.String()
}
