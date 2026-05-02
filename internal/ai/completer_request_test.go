package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureServer records the body of one request and replies with a minimal
// canned chat completion. Used to assert what we splice into the wire format.
func captureServer(t *testing.T, respUsage map[string]any) (*httptest.Server, *[]byte) {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		captured = body
		usage := map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		for k, v := range respUsage {
			usage[k] = v
		}
		resp := map[string]any{
			"id": "x", "object": "chat.completion", "created": 0, "model": "test",
			"choices": []map[string]any{{
				"index":   0,
				"message": map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": usage,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, b)
	}
	return m
}

func TestRequestShaping_OpenAICompat(t *testing.T) {
	srv, body := captureServer(t, nil)
	c := NewOpenAICompleter(ProviderOpenAICompat, srv.URL, "k", false)
	_, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "test",
		Messages: []Message{UserMessage("hi")},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, *body)
	if _, ok := got["provider"]; ok {
		t.Errorf("openai-compat must not splice provider object")
	}
	if _, ok := got["usage"]; ok {
		t.Errorf("openai-compat must not request detailed usage")
	}
	if _, ok := got["reasoning"]; ok {
		t.Errorf("reasoning omitted when ReasoningOff")
	}
}

func TestRequestShaping_OpenRouter_DefaultUsageOptIn(t *testing.T) {
	srv, body := captureServer(t, nil)
	c := NewOpenAICompleter(ProviderOpenRouter, srv.URL, "k", false)
	_, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "anthropic/claude",
		Messages: []Message{UserMessage("hi")},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, *body)
	usage, ok := got["usage"].(map[string]any)
	if !ok {
		t.Fatalf("openrouter must opt in to detailed usage; body=%s", *body)
	}
	if usage["include"] != true {
		t.Errorf("usage.include = %v, want true", usage["include"])
	}
	if _, ok := got["provider"]; ok {
		t.Errorf("provider object should be omitted when no routing prefs are set")
	}
}

func TestRequestShaping_OpenRouter_ProviderRouting(t *testing.T) {
	srv, body := captureServer(t, nil)
	c := NewOpenAICompleter(ProviderOpenRouter, srv.URL, "k", false)
	_, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "anthropic/claude",
		Messages: []Message{UserMessage("hi")},
		Provider: ProviderConfig{DenyDataCollection: true, NoFallbacks: true},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, *body)
	prov, ok := got["provider"].(map[string]any)
	if !ok {
		t.Fatalf("missing provider object; body=%s", *body)
	}
	if prov["data_collection"] != "deny" {
		t.Errorf("data_collection = %v, want deny", prov["data_collection"])
	}
	if prov["allow_fallbacks"] != false {
		t.Errorf("allow_fallbacks = %v, want false", prov["allow_fallbacks"])
	}
}

func TestRequestShaping_Reasoning(t *testing.T) {
	srv, body := captureServer(t, nil)
	c := NewOpenAICompleter(ProviderOpenAICompat, srv.URL, "k", false)
	_, err := c.Complete(context.Background(), CompletionRequest{
		Model:     "test",
		Messages:  []Message{UserMessage("hi")},
		Reasoning: ReasoningHigh,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, *body)
	r, ok := got["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("missing reasoning object; body=%s", *body)
	}
	if r["effort"] != "high" {
		t.Errorf("reasoning.effort = %v, want high", r["effort"])
	}
}

func TestCostExtraction_OpenRouter(t *testing.T) {
	srv, _ := captureServer(t, map[string]any{"cost": 0.0123})
	c := NewOpenAICompleter(ProviderOpenRouter, srv.URL, "k", false)
	res, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "anthropic/claude",
		Messages: []Message{UserMessage("hi")},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Cost != 0.0123 {
		t.Errorf("cost = %v, want 0.0123", res.Cost)
	}
}

func TestCostExtraction_OpenAICompatIgnoresCostField(t *testing.T) {
	srv, _ := captureServer(t, map[string]any{"cost": 0.0123})
	c := NewOpenAICompleter(ProviderOpenAICompat, srv.URL, "k", false)
	res, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "test",
		Messages: []Message{UserMessage("hi")},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Cost != 0 {
		t.Errorf("openai-compat must not surface cost; got %v", res.Cost)
	}
}
