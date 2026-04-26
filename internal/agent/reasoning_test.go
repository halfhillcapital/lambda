package agent

import (
	"testing"

	"github.com/openai/openai-go/packages/respjson"
)

func TestExtractReasoning(t *testing.T) {
	cases := []struct {
		name   string
		extras map[string]respjson.Field
		want   string
	}{
		{
			name:   "nil extras",
			extras: nil,
			want:   "",
		},
		{
			name: "reasoning_content set",
			extras: map[string]respjson.Field{
				"reasoning_content": respjson.NewField(`"step one"`),
			},
			want: "step one",
		},
		{
			name: "thinking field (ollama)",
			extras: map[string]respjson.Field{
				"thinking": respjson.NewField(`"deliberating"`),
			},
			want: "deliberating",
		},
		{
			name: "both populated — concatenated",
			extras: map[string]respjson.Field{
				"reasoning_content": respjson.NewField(`"a"`),
				"thinking":          respjson.NewField(`"b"`),
			},
			want: "ab",
		},
		{
			name: "null is ignored",
			extras: map[string]respjson.Field{
				"reasoning_content": respjson.NewField("null"),
			},
			want: "",
		},
		{
			name: "unrelated extras ignored",
			extras: map[string]respjson.Field{
				"some_other_field": respjson.NewField(`"x"`),
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractReasoning(tc.extras); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
