package ai

import (
	"reflect"
	"testing"

	"github.com/openai/openai-go/packages/respjson"
)

func TestOpenRouterProviderObject(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ProviderConfig
		want    any
		wantErr bool
	}{
		{
			name: "empty cfg returns nil so the field is omitted",
			cfg:  ProviderConfig{},
			want: nil,
		},
		{
			name: "deny data collection only",
			cfg:  ProviderConfig{DenyDataCollection: true},
			want: map[string]any{"data_collection": "deny"},
		},
		{
			name: "no fallbacks only",
			cfg:  ProviderConfig{NoFallbacks: true},
			want: map[string]any{"allow_fallbacks": false},
		},
		{
			name: "both flags combine",
			cfg:  ProviderConfig{DenyDataCollection: true, NoFallbacks: true},
			want: map[string]any{"data_collection": "deny", "allow_fallbacks": false},
		},
		{
			name: "raw JSON wins over structured fields",
			cfg: ProviderConfig{
				DenyDataCollection: true,
				NoFallbacks:        true,
				RawJSON:            `{"order": ["Anthropic"], "sort": "throughput"}`,
			},
			want: map[string]any{"order": []any{"Anthropic"}, "sort": "throughput"},
		},
		{
			name:    "raw JSON parse error surfaces",
			cfg:     ProviderConfig{RawJSON: "{not json"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := openRouterProviderObject(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestExtractCost(t *testing.T) {
	cases := []struct {
		name   string
		extras map[string]respjson.Field
		want   float64
	}{
		{"absent", nil, 0},
		{"null", map[string]respjson.Field{"cost": respjson.NewField("null")}, 0},
		{"present", map[string]respjson.Field{"cost": respjson.NewField("0.0042")}, 0.0042},
		{"unrelated extras", map[string]respjson.Field{"prompt_tokens": respjson.NewField("17")}, 0},
		{"unparseable", map[string]respjson.Field{"cost": respjson.NewField(`"not-a-number"`)}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractCost(tc.extras); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
