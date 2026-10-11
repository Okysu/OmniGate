package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Error classes (docs/contracts/protocol-adapter.md §5).
const (
	ErrInvalidRequest       = "invalid_request"
	ErrUnsupportedParameter = "unsupported_parameter"
	ErrAuthentication       = "authentication"
	ErrPermission           = "permission"
	// ErrAccountDisabled: the key is valid but its owner is disabled (phase7-api.md §2.1).
	ErrAccountDisabled     = "account_disabled"
	ErrModelNotFound       = "model_not_found"
	ErrQuotaExceeded       = "quota_exceeded"
	ErrQuotaExhausted      = "quota_exhausted"
	ErrInsufficientBalance = "insufficient_balance"
	ErrRateLimited         = "rate_limited"
	// ErrUserRequestLimit / ErrSpendLimit: the user group's requests-per-day
	// limit, or a spend limit (group or API key), is reached (phase8-api.md §2.2).
	ErrUserRequestLimit    = "user_request_limit"
	ErrSpendLimit          = "spend_limit_exceeded"
	ErrUpstreamRateLimited = "upstream_rate_limited"
	ErrUpstreamAuth        = "upstream_authentication"
	ErrUpstreamUnavailable = "upstream_unavailable"
	ErrUpstreamTimeout     = "upstream_timeout"
	ErrUpstreamInvalid     = "upstream_invalid_response"
	ErrUpstreamBadRequest  = "upstream_bad_request"
	// ErrContextLengthExceeded: the upstream rejected the input as longer than the
	// model's context window — the client's error, never the channel's.
	ErrContextLengthExceeded = "context_length_exceeded"
	ErrClientClosed          = "client_closed"
	ErrPluginError           = "plugin_error"
	ErrInternal              = "internal"
)

// GatewayError is a classified error rendered in the client's protocol.
type GatewayError struct {
	Class   string
	Status  int
	Message string
	// RetryAfter, when > 0, is sent as the retry-after header (seconds).
	RetryAfter time.Duration
}

func (e *GatewayError) Error() string { return e.Class + ": " + e.Message }

// NewError builds a GatewayError with the default status for its class.
func NewError(class, msg string) *GatewayError {
	return &GatewayError{Class: class, Status: defaultStatus(class), Message: msg}
}

func defaultStatus(class string) int {
	switch class {
	case ErrInvalidRequest, ErrUnsupportedParameter, ErrUpstreamBadRequest, ErrContextLengthExceeded:
		return http.StatusBadRequest
	case ErrAuthentication:
		return http.StatusUnauthorized
	case ErrPermission, ErrAccountDisabled:
		return http.StatusForbidden
	case ErrModelNotFound:
		return http.StatusNotFound
	case ErrInsufficientBalance:
		return http.StatusPaymentRequired
	case ErrQuotaExceeded, ErrQuotaExhausted, ErrRateLimited, ErrUpstreamRateLimited, ErrUserRequestLimit, ErrSpendLimit:
		return http.StatusTooManyRequests
	case ErrUpstreamTimeout:
		return http.StatusGatewayTimeout
	case ErrUpstreamAuth, ErrUpstreamUnavailable, ErrUpstreamInvalid, ErrPluginError:
		return http.StatusBadGateway
	case ErrClientClosed:
		return 499
	default:
		return http.StatusInternalServerError
	}
}

func openAIType(class string) (typ, code string) {
	switch class {
	case ErrInvalidRequest, ErrUpstreamBadRequest:
		return "invalid_request_error", class
	case ErrUnsupportedParameter:
		return "invalid_request_error", "unsupported_parameter"
	case ErrContextLengthExceeded:
		return "invalid_request_error", ErrContextLengthExceeded
	case ErrAuthentication:
		return "invalid_request_error", "invalid_api_key"
	case ErrPermission:
		return "invalid_request_error", "permission_denied"
	case ErrAccountDisabled:
		return "invalid_request_error", ErrAccountDisabled
	case ErrModelNotFound:
		return "invalid_request_error", "model_not_found"
	case ErrQuotaExceeded, ErrQuotaExhausted, ErrInsufficientBalance, ErrSpendLimit:
		return "insufficient_quota", class
	case ErrRateLimited, ErrUpstreamRateLimited, ErrUserRequestLimit:
		return "rate_limit_exceeded", class
	default:
		return "server_error", class
	}
}

func anthropicType(class string) string {
	switch class {
	case ErrInvalidRequest, ErrUnsupportedParameter, ErrUpstreamBadRequest, ErrContextLengthExceeded:
		return "invalid_request_error"
	case ErrAuthentication:
		return "authentication_error"
	case ErrPermission, ErrInsufficientBalance, ErrAccountDisabled:
		return "permission_error"
	case ErrModelNotFound:
		return "not_found_error"
	case ErrQuotaExceeded, ErrQuotaExhausted, ErrRateLimited, ErrUpstreamRateLimited, ErrUserRequestLimit, ErrSpendLimit:
		return "rate_limit_error"
	case ErrUpstreamUnavailable:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// EncodeError renders e in the given client dialect.
func EncodeError(dialect string, e *GatewayError) []byte {
	if dialect == Anthropic {
		b, _ := json.Marshal(map[string]any{
			"type":  "error",
			"error": map[string]string{"type": anthropicType(e.Class), "message": e.Message},
		})
		return b
	}
	typ, code := openAIType(e.Class)
	b, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": e.Message, "type": typ, "param": nil, "code": code},
	})
	return b
}

// EncodeStreamError renders e as an in-stream error event for the dialect.
func EncodeStreamError(dialect string, e *GatewayError) []byte {
	if dialect == Anthropic {
		return FormatEvent("error", EncodeError(dialect, e))
	}
	return FormatEvent("", EncodeError(dialect, e))
}

var secretPatterns = regexp.MustCompile(`(?i)(sk|og|ak|pk|rk)-[a-z0-9_\-]{8,}|bearer\s+[a-z0-9._\-]{8,}|AIza[0-9A-Za-z_\-]{20,}|[A-Za-z0-9_\-]{40,}`)

// Redact removes credential-looking substrings from upstream messages.
func Redact(s string) string {
	return secretPatterns.ReplaceAllString(s, "[redacted]")
}

// UpstreamErrorMessage extracts a short, redacted message from an upstream
// error body in either OpenAI or Anthropic format.
func UpstreamErrorMessage(body []byte) string {
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	msg := ""
	if json.Unmarshal(body, &env) == nil && len(env.Error) > 0 {
		var obj struct {
			Message string `json:"message"`
		}
		var s string
		if json.Unmarshal(env.Error, &obj) == nil && obj.Message != "" {
			msg = obj.Message
		} else if json.Unmarshal(env.Error, &s) == nil {
			msg = s
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return Redact(msg)
}

func randomID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// contextLengthRe matches the context-window errors of OpenAI, Anthropic,
// Gemini, GLM, DeepSeek and the proxies in front of them.
var contextLengthRe = regexp.MustCompile(`(?i)context[ _-]?(window|length)|maximum context|prompt is too long|input is too long|input (token count|length) exceeds|exceeds the (model'?s )?(maximum|context|max)|reduce the length of (the )?(messages|input|prompt)`)

// IsContextLengthMessage reports whether an upstream error message says the
// input does not fit the model's context window.
func IsContextLengthMessage(msg string) bool { return contextLengthRe.MatchString(msg) }

// IsContextLengthError reports whether an upstream error body (OpenAI or
// Anthropic format, or plain text) is a context-window error.
func IsContextLengthError(body []byte) bool {
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && len(env.Error) > 0 {
		var obj struct {
			Code    any    `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(env.Error, &obj) == nil {
			if c, ok := obj.Code.(string); ok && (c == "context_length_exceeded" || c == "model_context_window_exceeded") {
				return true
			}
			return IsContextLengthMessage(obj.Message)
		}
		var s string
		if json.Unmarshal(env.Error, &s) == nil {
			return IsContextLengthMessage(s)
		}
	}
	return IsContextLengthMessage(string(body))
}

// UpstreamError extracts the error object of a JSON body that carries one
// ({"error": …}); ok is false for any other body.
func UpstreamError(body []byte) (msg string, ok bool) {
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil || len(env.Error) == 0 || string(env.Error) == "null" {
		return "", false
	}
	return UpstreamErrorMessage(body), true
}

// HasResponsePayload reports whether a JSON body carries a real response next
// to an "error" field (choices, output, content, data …), so a body like
// Responses' {"error": null, "output": …} is never mistaken for an error.
func HasResponsePayload(body []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	for _, k := range []string{"choices", "output", "content", "data", "embedding"} {
		if v, ok := m[k]; ok && string(v) != "null" {
			return true
		}
	}
	return false
}
