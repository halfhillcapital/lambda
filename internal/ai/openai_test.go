package ai

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/respjson"
)

func TestIsTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"500", &openai.Error{StatusCode: 500}, true},
		{"503", &openai.Error{StatusCode: 503}, true},
		{"599", &openai.Error{StatusCode: 599}, true},
		{"408 request timeout", &openai.Error{StatusCode: 408}, true},
		{"429 rate limit", &openai.Error{StatusCode: 429}, true},
		{"400 bad request", &openai.Error{StatusCode: 400}, false},
		{"401 auth", &openai.Error{StatusCode: 401}, false},
		{"404 not found", &openai.Error{StatusCode: 404}, false},
		{"io.EOF", io.EOF, true},
		{"io.ErrUnexpectedEOF", io.ErrUnexpectedEOF, true},
		{"net.OpError", &net.OpError{Op: "read", Err: errors.New("dial fail")}, true},
		{"ECONNRESET wrapped", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, true},
		{"plain error", errors.New("nope"), false},
		{"context canceled", context.Canceled, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isTransient(c.err); got != c.want {
				t.Errorf("isTransient(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestJitter(t *testing.T) {
	const base = 1000 * time.Millisecond
	for range 100 {
		got := jitter(base)
		if got < 750*time.Millisecond || got > 1250*time.Millisecond {
			t.Fatalf("jitter(%v) = %v, want within ±25%%", base, got)
		}
	}
	if got := jitter(0); got != 0 {
		t.Errorf("jitter(0) = %v, want 0", got)
	}
}

func TestWithTransientRetry(t *testing.T) {
	withFastBackoffs(t, 1*time.Millisecond, 1*time.Millisecond)

	t.Run("succeeds first try without retry", func(t *testing.T) {
		calls := 0
		res, err := withTransientRetry(context.Background(), func() (int, error) {
			calls++
			return 42, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if res != 42 {
			t.Errorf("got %d", res)
		}
		if calls != 1 {
			t.Errorf("called %d times, want 1", calls)
		}
	})

	t.Run("retries on transient then succeeds", func(t *testing.T) {
		calls := 0
		res, err := withTransientRetry(context.Background(), func() (int, error) {
			calls++
			if calls < 3 {
				return 0, &openai.Error{StatusCode: 503}
			}
			return 7, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if res != 7 {
			t.Errorf("got %d", res)
		}
		if calls != 3 {
			t.Errorf("called %d times, want 3", calls)
		}
	})

	t.Run("does not retry non-transient", func(t *testing.T) {
		calls := 0
		_, err := withTransientRetry(context.Background(), func() (int, error) {
			calls++
			return 0, &openai.Error{StatusCode: 401}
		})
		if err == nil {
			t.Fatal("expected error")
		}
		if calls != 1 {
			t.Errorf("called %d times, want 1", calls)
		}
	})

	t.Run("gives up after exhausting retries", func(t *testing.T) {
		calls := 0
		_, err := withTransientRetry(context.Background(), func() (int, error) {
			calls++
			return 0, &openai.Error{StatusCode: 500}
		})
		if err == nil {
			t.Fatal("expected error after exhausting retries")
		}
		want := len(retryBackoffs) + 1
		if calls != want {
			t.Errorf("called %d times, want %d", calls, want)
		}
	})

	t.Run("respects ctx cancellation during backoff", func(t *testing.T) {
		// Use longer backoffs so ctx cancellation lands during the sleep.
		saved := retryBackoffs
		retryBackoffs = []time.Duration{500 * time.Millisecond, 500 * time.Millisecond}
		defer func() { retryBackoffs = saved }()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		calls := 0
		start := time.Now()
		_, err := withTransientRetry(ctx, func() (int, error) {
			calls++
			return 0, &openai.Error{StatusCode: 500}
		})
		if err == nil {
			t.Error("expected error")
		}
		if calls > 2 {
			t.Errorf("retried after ctx cancel: %d calls", calls)
		}
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Errorf("took %v, expected to bail out near ctx deadline", elapsed)
		}
	})
}

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

// withFastBackoffs swaps retryBackoffs to 1ms delays for the duration of the
// test, restoring the original on cleanup.
func withFastBackoffs(t *testing.T, delays ...time.Duration) {
	t.Helper()
	saved := retryBackoffs
	retryBackoffs = delays
	t.Cleanup(func() { retryBackoffs = saved })
}
