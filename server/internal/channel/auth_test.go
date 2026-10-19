package channel

import (
	"context"
	"testing"
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
