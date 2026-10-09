package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"omnigate/internal/plugin/engine"
)

// Custom-protocol limits (phase9-api.md §2).
const (
	// StreamCallTimeout bounds each parseStream / endStream call.
	StreamCallTimeout = 50 * time.Millisecond
	// ProtocolBudget is the total JS execution time of one request.
	ProtocolBudget = 5 * time.Second
	// maxParseResponseTimeout caps parseResponse's size-scaled timeout.
	maxParseResponseTimeout = time.Second
)

// ParseResponseTimeout is parseResponse's timeout: 50 ms plus 10 ms per
// 64 KiB of response body, at most 1 s (large bodies take longer to parse).
func ParseResponseTimeout(bodyBytes int) time.Duration {
	return min(HookTimeout+time.Duration(bodyBytes/(64<<10))*10*time.Millisecond, maxParseResponseTimeout)
}

// CustomProtocol reports whether the plugin implements the upstream protocol
// itself (manifest protocol "custom").
func (l *Loaded) CustomProtocol() bool { return l.prog != nil && l.Manifest.CustomProtocol() }

// UpstreamResponse is what parseResponse / normalizeError receive.
type UpstreamResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// NewUpstreamResponse builds the hook payload from an HTTP response.
func NewUpstreamResponse(status int, h http.Header, body []byte) UpstreamResponse {
	headers := map[string]string{}
	for k, v := range h {
		headers[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	return UpstreamResponse{Status: status, Headers: headers, Body: strings.ToValidUTF8(string(body), "�")}
}

// HTTPRequest is the upstream request produced by buildRequest (and
// signRequest), with secret handles replaced and the URL resolved.
type HTTPRequest struct {
	Method  string
	URL     *url.URL
	Headers map[string]string
	Body    []byte // nil = no body
}

// NormalizedError is normalizeError's result (or a CanonicalResponse error).
type NormalizedError struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// ProtocolSession runs one request's custom-protocol hooks in a pinned
// runtime (ADR-0002 §2) with the per-request JS time budget.
type ProtocolSession struct {
	l   *Loaded
	ch  ChannelEnv
	env *engine.Env
	s   *engine.Session
	// timeouts are scaled in the editor's test runner.
	scale int
}

// NewProtocolSession starts a session for one request through channel ch.
func (l *Loaded) NewProtocolSession(ctx context.Context, ch ChannelEnv) (*ProtocolSession, error) {
	return l.newProtocolSession(ctx, ch, nil, ProtocolBudget, 1)
}

func (l *Loaded) newProtocolSession(ctx context.Context, ch ChannelEnv, mocks []engine.MockFetch, budget time.Duration, scale int) (*ProtocolSession, error) {
	if !l.CustomProtocol() {
		return nil, &engine.Error{Kind: "missing", Message: "插件没有实现自定义协议"}
	}
	env := l.env(ch, "", mocks)
	s, err := l.prog.NewSession(ctx, env, budget)
	if err != nil {
		return nil, err
	}
	return &ProtocolSession{l: l, ch: ch, env: env, s: s, scale: scale}, nil
}

func (ps *ProtocolSession) mult() time.Duration { return time.Duration(ps.scale) }

// Close releases the pinned runtime.
func (ps *ProtocolSession) Close() { ps.s.Close() }

// Used is the JS execution time consumed by the request so far.
func (ps *ProtocolSession) Used() time.Duration { return ps.s.Used() }

// Logs returns the plugin's log lines.
func (ps *ProtocolSession) Logs() []engine.LogLine { return ps.s.Logs() }

// Fetches returns the plugin's og.fetch calls.
func (ps *ProtocolSession) Fetches() []engine.FetchRecord { return ps.s.Fetches() }

func outputLimit(in int) int { return min(2*in+(1<<20), 80<<20) }

// customRequest is the JSON shape of buildRequest's result.
type customRequest struct {
	Dialect string            `json:"dialect,omitempty"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// BuildRequest runs buildRequest(chatRequest, ctx) and then signRequest (when
// declared) and resolves the result into an HTTP request. The raw JSON result
// (after secret substitution) is returned too (editor tests show it).
func (ps *ProtocolSession) BuildRequest(ctx context.Context, chatRequest []byte) (*HTTPRequest, json.RawMessage, error) {
	limit := outputLimit(len(chatRequest))
	out, err := ps.s.Call(ctx, []string{"buildRequest"}, HookTimeout*ps.mult(), limit, json.RawMessage(chatRequest), ps.env.Context)
	if err != nil {
		return nil, nil, err
	}
	var cr customRequest
	if err := json.Unmarshal(out, &cr); err != nil || string(out) == "null" {
		return nil, nil, &engine.Error{Kind: "output", Message: "buildRequest 必须返回 {method, url, headers, body}"}
	}
	if ps.l.Manifest.HasHook("signRequest") {
		cr.Dialect = ProtocolCustom
		in, _ := json.Marshal(cr)
		if out, err = ps.s.Call(ctx, []string{"signRequest"}, HookTimeout*ps.mult(), limit, json.RawMessage(in), ps.env.Context); err != nil {
			return nil, nil, err
		}
		cr = customRequest{}
		if err := json.Unmarshal(out, &cr); err != nil || string(out) == "null" {
			return nil, nil, &engine.Error{Kind: "output", Message: "signRequest 必须返回请求对象"}
		}
	}
	if out, err = ps.s.SubstituteHeaders(out); err != nil {
		return nil, nil, err
	}
	cr = customRequest{}
	_ = json.Unmarshal(out, &cr)
	req, err := ps.resolve(cr)
	if err != nil {
		return nil, nil, err
	}
	return req, out, nil
}

// resolve validates method, URL and headers and encodes the body.
func (ps *ProtocolSession) resolve(cr customRequest) (*HTTPRequest, error) {
	bad := func(format string, a ...any) error {
		return &engine.Error{Kind: "output", Message: fmt.Sprintf(format, a...)}
	}
	method := strings.ToUpper(strings.TrimSpace(cr.Method))
	switch method {
	case "":
		method = http.MethodPost
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return nil, bad("不支持的 HTTP 方法 %q", cr.Method)
	}
	base, err := url.Parse(ps.ch.BaseURL)
	if err != nil || base.Host == "" {
		return nil, bad("渠道的 baseUrl 无效")
	}
	var u *url.URL
	switch {
	case strings.HasPrefix(cr.URL, "/") && !strings.HasPrefix(cr.URL, "//"):
		// Relative to the channel base URL (path and query appended).
		u, err = url.Parse(strings.TrimRight(ps.ch.BaseURL, "/") + cr.URL)
		if err != nil {
			return nil, bad("url 无效")
		}
	default:
		u, err = url.Parse(cr.URL)
		if err != nil || u.Host == "" || u.User != nil {
			return nil, bad("url 必须是以 / 开头的路径（相对渠道 baseUrl）或 http(s) 绝对地址")
		}
		sameHost := strings.EqualFold(u.Host, base.Host)
		switch {
		case u.Scheme == "https":
		case u.Scheme == "http" && (sameHost && base.Scheme == "http" || ps.ch.AllowPrivate):
		default:
			return nil, bad("url 必须使用 https")
		}
		if !sameHost && !HostAllowed(u.Hostname(), ps.l.Manifest.Permissions.Network, base.Hostname()) {
			return nil, bad("主机 %s 未在 permissions.network 中声明", u.Hostname())
		}
	}
	u.Fragment = ""
	headers := map[string]string{}
	for k, v := range cr.Headers {
		if strings.ContainsAny(k, "\r\n:") || strings.ContainsAny(v, "\r\n") {
			return nil, bad("请求头 %q 无效", k)
		}
		headers[k] = v
	}
	var body []byte
	if b := bytes.TrimSpace(cr.Body); len(b) > 0 && string(b) != "null" {
		var str string
		if json.Unmarshal(b, &str) == nil {
			body = []byte(str)
		} else {
			body = b
			if !hasHeader(headers, "Content-Type") {
				headers["Content-Type"] = "application/json"
			}
		}
	}
	return &HTTPRequest{Method: method, URL: u, Headers: headers, Body: body}, nil
}

func hasHeader(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// ParseResponse runs parseResponse on a successful unary response and returns
// the CanonicalResponse (Chat Completions subset) JSON.
func (ps *ProtocolSession) ParseResponse(ctx context.Context, res UpstreamResponse) (json.RawMessage, error) {
	return ps.s.Call(ctx, []string{"parseResponse"}, ParseResponseTimeout(len(res.Body))*ps.mult(), outputLimit(len(res.Body)), res, ps.env.Context)
}

// ParseStream runs parseStream(chunk, state, ctx) and returns the event array JSON.
func (ps *ProtocolSession) ParseStream(ctx context.Context, chunk []byte) (json.RawMessage, error) {
	out, err := ps.s.Call(ctx, []string{"parseStream"}, StreamCallTimeout*ps.mult(), outputLimit(len(chunk)), engine.Bytes(chunk), engine.State, ps.env.Context)
	if err != nil {
		return nil, err
	}
	return eventsOrEmpty(out, "parseStream")
}

// EndStream runs endStream(state, ctx) when declared (nil otherwise).
func (ps *ProtocolSession) EndStream(ctx context.Context) (json.RawMessage, error) {
	if !ps.l.Manifest.HasHook("endStream") {
		return nil, nil
	}
	out, err := ps.s.Call(ctx, []string{"endStream"}, StreamCallTimeout*ps.mult(), 1<<20, engine.State, ps.env.Context)
	if err != nil {
		return nil, err
	}
	return eventsOrEmpty(out, "endStream")
}

func eventsOrEmpty(out json.RawMessage, hook string) (json.RawMessage, error) {
	switch t := bytes.TrimSpace(out); {
	case len(t) == 0, string(t) == "null", string(t) == "undefined":
		return json.RawMessage("[]"), nil
	case t[0] != '[':
		return nil, &engine.Error{Kind: "output", Message: hook + " 必须返回事件数组"}
	}
	return out, nil
}

// HasNormalizeError reports whether normalizeError is declared.
func (ps *ProtocolSession) HasNormalizeError() bool { return ps.l.Manifest.HasHook("normalizeError") }

// NormalizeError runs normalizeError on an error response.
func (ps *ProtocolSession) NormalizeError(ctx context.Context, res UpstreamResponse) (*NormalizedError, json.RawMessage, error) {
	out, err := ps.s.Call(ctx, []string{"normalizeError"}, HookTimeout*ps.mult(), 64<<10, res, ps.env.Context)
	if err != nil {
		return nil, nil, err
	}
	var ne NormalizedError
	if json.Unmarshal(out, &ne) != nil || ne.Status < 100 || ne.Status > 599 {
		return nil, out, &engine.Error{Kind: "output", Message: "normalizeError 必须返回 {status: 100–599, message}"}
	}
	return &ne, out, nil
}
