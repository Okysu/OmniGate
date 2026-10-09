package gateway

import (
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
