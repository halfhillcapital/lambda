package config

import (
	"testing"

	"lambda/internal/ai"
)

func TestParseProvider(t *testing.T) {
	cases := []struct {
		in      string
		want    ai.Provider
		wantErr bool
	}{
		{"", ai.ProviderOpenAICompat, false},
		{"openai-compat", ai.ProviderOpenAICompat, false},
		{"openai", ai.ProviderOpenAICompat, false},
		{"OpenRouter", ai.ProviderOpenRouter, false},
		{"openrouter", ai.ProviderOpenRouter, false},
		{"anthropic", "", true},
	}
	for _, c := range cases {
		got, err := parseProvider(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseProvider(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseProvider(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseProvider(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseReasoning(t *testing.T) {
	cases := []struct {
		in      string
		want    ai.ReasoningEffort
		wantErr bool
	}{
		{"", ai.ReasoningOff, false},
		{"off", ai.ReasoningOff, false},
		{"LOW", ai.ReasoningLow, false},
		{"medium", ai.ReasoningMedium, false},
		{"high", ai.ReasoningHigh, false},
		{"extreme", "", true},
	}
	for _, c := range cases {
		got, err := parseReasoning(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseReasoning(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseReasoning(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseReasoning(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestResolveAPIKey_OpenRouterPrefersOpenRouterEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-x")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	if got := resolveAPIKey(ai.ProviderOpenRouter); got != "sk-or-x" {
		t.Errorf("got %q, want sk-or-x", got)
	}
}

func TestResolveAPIKey_OpenRouterFallsBackToOpenAIEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	if got := resolveAPIKey(ai.ProviderOpenRouter); got != "sk-openai" {
		t.Errorf("got %q, want sk-openai (fallback)", got)
	}
}

func TestResolveAPIKey_OpenAICompatIgnoresOpenRouterEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-x")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	if got := resolveAPIKey(ai.ProviderOpenAICompat); got != "sk-openai" {
		t.Errorf("got %q, want sk-openai", got)
	}
}

func TestResolveAPIKey_DefaultLocal(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	if got := resolveAPIKey(ai.ProviderOpenAICompat); got != defaultAPIKey {
		t.Errorf("got %q, want default %q", got, defaultAPIKey)
	}
}
