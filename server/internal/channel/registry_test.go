package channel

import (
	"context"
	"net/http"
	"testing"

	"omnigate/internal/protocol"
)

func TestPassHeaders(t *testing.T) {
	rt := &Runtime{Channel: Channel{Type: TypeOpenAI, BaseURL: "http://up.test/v1", Config: Config{Headers: map[string]string{"X-Codex-Beta-Features": "channel"}}},
		APIKey: "sk-channel"}
	client := http.Header{}
	for k, v := range map[string]string{"Session_id": "s-1", "X-Codex-Turn-Metadata": "{\"turn\":1}", "User-Agent": "codex_cli_rs/0.50",
		"X-Codex-Beta-Features": "client", "Authorization": "Bearer og-client-key", "Cookie": "a=b", "X-Not-Listed": "x"} {
		client.Set(k, v)
	}
	pass := &PassHeaders{Names: []string{"Session_id", "X-Codex-Turn-Metadata", "User-Agent", "X-Codex-Beta-Features", "Authorization", "Cookie", "X-Absent"}, KeepOrigin: true}
	req, err := rt.NewRequest(context.Background(), protocol.OpenAIResponses, []byte(`{}`), client, "OmniGate/test", &RequestOverride{Pass: pass})
	if err != nil {
		t.Fatal(err)
	}
	h := req.Header
	if h.Get("Session_id") != "s-1" || h.Get("X-Codex-Turn-Metadata") != `{"turn":1}` {
		t.Errorf("session headers not passed: %v", h)
	}
	if _, ok := h["Session_id"]; !ok {
		t.Error("underscore header name must be kept")
	}
	// The gateway's default User-Agent is not channel configuration.
	if h.Get("User-Agent") != "codex_cli_rs/0.50" {
		t.Errorf("User-Agent = %q", h.Get("User-Agent"))
	}
	// keep_origin: the channel's explicit header wins.
	if h.Get("X-Codex-Beta-Features") != "channel" {
		t.Errorf("keep_origin: %q", h.Get("X-Codex-Beta-Features"))
	}
	// Credentials and cookies are never copied, unlisted headers neither.
	if h.Get("Authorization") != "Bearer sk-channel" || h.Get("Cookie") != "" || h.Get("X-Not-Listed") != "" || h.Get("X-Absent") != "" {
		t.Errorf("forbidden or unlisted header forwarded: %v", h)
	}

	// Without keep_origin the client value overwrites the channel's.
	pass.KeepOrigin = false
	req, _ = rt.NewRequest(context.Background(), protocol.OpenAIResponses, []byte(`{}`), client, "OmniGate/test", &RequestOverride{Pass: pass})
	if req.Header.Get("X-Codex-Beta-Features") != "client" {
		t.Errorf("overwrite: %q", req.Header.Get("X-Codex-Beta-Features"))
	}

	// A plugin hook's header counts as explicit configuration.
	pass.KeepOrigin = true
	req, _ = rt.NewRequest(context.Background(), protocol.OpenAIResponses, []byte(`{}`), client, "OmniGate/test",
		&RequestOverride{Headers: map[string]string{"session_id": "hook"}, Pass: pass})
	if req.Header.Get("Session_id") != "hook" {
		t.Errorf("hook header: %q", req.Header.Get("Session_id"))
	}

	// No pass list: nothing but the defaults.
	req, _ = rt.NewRequest(context.Background(), protocol.OpenAIResponses, []byte(`{}`), client, "OmniGate/test")
	if req.Header.Get("Session_id") != "" || req.Header.Get("User-Agent") != "OmniGate/test" {
		t.Errorf("headers passed without a rule: %v", req.Header)
	}
}

func TestFillHeaders(t *testing.T) {
	rt := &Runtime{Channel: Channel{Type: TypeOpenAI, BaseURL: "http://up.test/v1", Config: Config{Headers: map[string]string{"X-Fixed": "channel"}}}, APIKey: "sk-channel"}
	fill := map[string]string{"Session_id": "derived", "X-Fixed": "derived", "Authorization": "Bearer x"}
	// Set when absent; channel config and credentials win.
	req, _ := rt.NewRequest(context.Background(), protocol.OpenAIChat, []byte(`{}`), http.Header{}, "OmniGate/test", &RequestOverride{Fill: fill})
	if h := req.Header; h.Get("Session_id") != "derived" || h.Get("X-Fixed") != "channel" || h.Get("Authorization") != "Bearer sk-channel" {
		t.Errorf("fill: %v", h)
	}
	// A passed client header is never replaced.
	client := http.Header{}
	client.Set("Session_id", "client")
	req, _ = rt.NewRequest(context.Background(), protocol.OpenAIChat, []byte(`{}`), client, "OmniGate/test",
		&RequestOverride{Pass: &PassHeaders{Names: []string{"Session_id"}, KeepOrigin: true}, Fill: fill})
	if v := req.Header.Values("Session_id"); len(v) != 1 || v[0] != "client" {
		t.Errorf("client header replaced: %v", v)
	}
	// A plugin hook's header counts as present.
	req, _ = rt.NewRequest(context.Background(), protocol.OpenAIChat, []byte(`{}`), client, "OmniGate/test",
		&RequestOverride{Headers: map[string]string{"session_id": "hook"}, Fill: fill})
	if req.Header.Get("Session_id") != "hook" {
		t.Errorf("hook header replaced: %q", req.Header.Get("Session_id"))
	}
}
