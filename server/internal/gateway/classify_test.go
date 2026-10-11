package gateway

import (
	"strings"
	"testing"

	"omnigate/internal/protocol"
	"omnigate/internal/routing"
)

func TestClassifyStatusRetryClasses(t *testing.T) {
	cases := []struct {
		status int
		class  string
		err    string
		health bool
	}{
		{429, routing.RetryRateLimit, protocol.ErrUpstreamRateLimited, true},
		{500, routing.RetryServerError, protocol.ErrUpstreamUnavailable, true},
		{503, routing.RetryServerError, protocol.ErrUpstreamUnavailable, true},
		{401, routing.RetryAuthError, protocol.ErrUpstreamAuth, true},
		{403, routing.RetryAuthError, protocol.ErrUpstreamAuth, true},
		{404, routing.RetryNotFound, protocol.ErrModelNotFound, false},
		{408, routing.RetryTimeout, protocol.ErrUpstreamTimeout, true},
		{302, routing.RetryServerError, protocol.ErrUpstreamInvalid, true},
		{400, routing.RetryClientError, protocol.ErrUpstreamBadRequest, false},
		{402, routing.RetryClientError, protocol.ErrUpstreamBadRequest, false},
		{422, routing.RetryClientError, protocol.ErrUpstreamBadRequest, false},
	}
	for _, c := range cases {
		e, class, health := classifyStatus(c.status, "upstream: x")
		if class != c.class || e.Class != c.err || health != c.health {
			t.Errorf("status %d = (%s, %q, %v), want (%s, %q, %v)", c.status, e.Class, class, health, c.err, c.class, c.health)
		}
	}
	if e, _, _ := classifyStatus(422, "x"); e.Status != 400 {
		t.Errorf("422 maps to status %d", e.Status)
	}
	// The default policy keeps today's behaviour: everything except other 4xx.
	def := routing.Retry{MaxAttempts: 3, RetryOn: routing.DefaultRetryClasses}
	for _, c := range cases {
		if want := c.class != routing.RetryClientError; def.Allows(c.class) != want {
			t.Errorf("default retry policy allows %s = %v", c.class, !want)
		}
	}
	// The classification depends on the status code only, never on the message.
	for _, msg := range []string{"upstream: model not found", "upstream: insufficient balance", "upstream: rate limit"} {
		if _, class, _ := classifyStatus(400, msg); class != routing.RetryClientError {
			t.Errorf("400 %q classified as %s", msg, class)
		}
	}
}

func TestClassifyContextLength(t *testing.T) {
	bodies := []string{
		// AxonHub / Codex (Responses WebSocket), OpenAI, Anthropic, plain text.
		`{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"","param":"input"}}`,
		`{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens. Please reduce the length of the messages.","type":"invalid_request_error","code":null}}`,
		`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`,
		`Input token count exceeds the maximum number of tokens allowed`,
	}
	for _, status := range []int{400, 500, 502} {
		for _, b := range bodies {
			e, class, health := classifyUpstream(status, []byte(b))
			if e.Class != protocol.ErrContextLengthExceeded || e.Status != 400 || class != "" || health {
				t.Errorf("status %d %s = (%s %d, %q, %v)", status, b[:40], e.Class, e.Status, class, health)
			}
		}
	}
	// Rate limits and credential errors keep their class even when they
	// mention the context window; ordinary 5xx stay server errors.
	if _, class, health := classifyUpstream(429, []byte(bodies[0])); class != routing.RetryRateLimit || !health {
		t.Error("429 must stay a rate limit")
	}
	if e, class, health := classifyUpstream(500, []byte(`{"error":{"message":"internal error"}}`)); e.Class != protocol.ErrUpstreamUnavailable || class != routing.RetryServerError || !health {
		t.Error("plain 500 must stay a server error")
	}
	if e, _, _ := classifyMessage(502, "upstream: prompt is too long: 300000 tokens"); e.Class != protocol.ErrContextLengthExceeded {
		t.Error("plugin message not classified")
	}
	// The client sees an OpenAI-compatible invalid_request_error.
	out := string(protocol.EncodeError(protocol.OpenAIChat, protocol.NewError(protocol.ErrContextLengthExceeded, "x")))
	if !strings.Contains(out, `"type":"invalid_request_error"`) || !strings.Contains(out, `"code":"context_length_exceeded"`) {
		t.Errorf("encoded = %s", out)
	}
}
