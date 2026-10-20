package channel

import (
	"context"
	"testing"
	"time"
)

// Anthropic channels send the key as x-api-key to Anthropic itself and, by
// default, also as a Bearer token to third-party Anthropic-compatible
// upstreams; the mode can be forced.
func TestAnthropicAuthHeaders(t *testing.T) {
	cases := []struct {
		base, mode     string
		apiKey, bearer bool
	}{
		{"https://api.anthropic.com/v1", "", true, false},
		{"https://open.bigmodel.cn/api/anthropic/v1", "", true, true},
		{"https://relay.example.com/v1", AuthHeaderAuto, true, true},
		{"https://relay.example.com/v1", AuthHeaderAPIKey, true, false},
		{"https://relay.example.com/v1", AuthHeaderBearer, false, true},
		{"https://api.anthropic.com/v1", AuthHeaderBoth, true, true},
	}
	for _, c := range cases {
		rt := &Runtime{Channel: Channel{Type: TypeAnthropic, BaseURL: c.base, Config: Config{AuthHeader: c.mode}}, APIKey: "sk-test"}
		req, err := rt.NewRequest(context.Background(), "anthropic.messages", []byte(`{}`), nil, "test")
		if err != nil {
			t.Fatal(err)
		}
		gotKey, gotBearer := req.Header.Get("x-api-key") == "sk-test", req.Header.Get("Authorization") == "Bearer sk-test"
		if gotKey != c.apiKey || gotBearer != c.bearer {
			t.Errorf("%s %q: x-api-key=%v bearer=%v", c.base, c.mode, gotKey, gotBearer)
		}
	}
}

// timeoutSeconds: 0 = the default 60 s, -1 = no response-header timeout.
func TestRuntimeTimeout(t *testing.T) {
	for secs, want := range map[int]time.Duration{0: time.Minute, 300: 5 * time.Minute, 7200: 2 * time.Hour, -1: 0} {
		rt := &Runtime{Channel: Channel{Config: Config{TimeoutSeconds: secs}}}
		if got := rt.Timeout(); got != want {
			t.Errorf("timeoutSeconds %d: %s, want %s", secs, got, want)
		}
	}
	fired := make(chan struct{}, 1)
	stop := (&Runtime{Channel: Channel{Config: Config{TimeoutSeconds: -1}}}).HeaderTimer(func() { fired <- struct{}{} })
	defer stop()
	select {
	case <-fired:
		t.Fatal("no timeout must never fire")
	case <-time.After(50 * time.Millisecond):
	}
}
