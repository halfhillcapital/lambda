package ai

// Provider selects which backend mode the completer runs in: request shaping,
// the API-key env var that fronts it, and which response fields to read.
// All providers speak OpenAI-compatible chat/completions; the Provider value
// only enables small per-backend deltas.
type Provider string

const (
	// ProviderOpenAICompat is the default: any server that implements the
	// OpenAI chat/completions API (OpenAI itself, Ollama, LM Studio, vLLM,
	// TGI, and self-hosted proxies).
	ProviderOpenAICompat Provider = "openai-compat"

	// ProviderOpenRouter is the OpenRouter aggregator at openrouter.ai.
	// Adds: provider-routing object on the request, usage:{include:true} for
	// cost reporting, and reads cost out of the response usage block.
	ProviderOpenRouter Provider = "openrouter"
)

// DefaultBaseURL returns the canonical base URL for a provider, used when the
// user has not set one explicitly. ProviderOpenAICompat returns "" — there is
// no canonical default, so config.go's local-Ollama default applies.
func (p Provider) DefaultBaseURL() string {
	switch p {
	case ProviderOpenRouter:
		return "https://openrouter.ai/api/v1"
	}
	return ""
}
