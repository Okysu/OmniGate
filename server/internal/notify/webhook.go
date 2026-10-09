package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"omnigate/internal/platform/netguard"
)

const webhookTimeout = 10 * time.Second

// webhookRetries are the delays before the retries of a failed webhook (§3:
// 1, 5 and 30 minutes, i.e. at most 4 attempts).
var webhookRetries = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// SignatureHeader carries the HMAC of the request body (json format, and
// every other format when a secret is set).
const SignatureHeader = "X-OmniGate-Signature"

// Payload is the json webhook body (§3).
type Payload struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	URL       string         `json:"url"`
	Data      map[string]any `json:"data"`
	CreatedAt time.Time      `json:"createdAt"`
}

// Sign returns "sha256=<hex HMAC-SHA256(secret, body)>".
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func markdownText(p Payload) string {
	s := p.Body
	if p.URL != "" {
		s += "\n\n[查看详情](" + p.URL + ")"
	}
	return s
}

// webhookBody converts p to the request body of format; it may also return
// extra query parameters (DingTalk signing).
func webhookBody(format string, p Payload, secret string, now time.Time) ([]byte, url.Values, error) {
	ts := now.UnixMilli()
	switch format {
	case "feishu":
		msg := map[string]any{"msg_type": "interactive", "card": map[string]any{
			"header":   map[string]any{"title": map[string]any{"tag": "plain_text", "content": p.Title}},
			"elements": []any{map[string]any{"tag": "markdown", "content": markdownText(p)}},
		}}
		if secret != "" {
			sec := strconv.FormatInt(ts/1000, 10)
			m := hmac.New(sha256.New, []byte(sec+"\n"+secret))
			msg["timestamp"], msg["sign"] = sec, base64.StdEncoding.EncodeToString(m.Sum(nil))
		}
		b, err := json.Marshal(msg)
		return b, nil, err
	case "dingtalk":
		b, err := json.Marshal(map[string]any{"msgtype": "markdown", "markdown": map[string]any{
			"title": p.Title, "text": "### " + p.Title + "\n\n" + markdownText(p)}})
		var q url.Values
		if secret != "" {
			m := hmac.New(sha256.New, []byte(secret))
			m.Write([]byte(strconv.FormatInt(ts, 10) + "\n" + secret))
			q = url.Values{"timestamp": {strconv.FormatInt(ts, 10)}, "sign": {base64.StdEncoding.EncodeToString(m.Sum(nil))}}
		}
		return b, q, err
	case "wecom":
		b, err := json.Marshal(map[string]any{"msgtype": "markdown", "markdown": map[string]any{
			"content": "**" + p.Title + "**\n" + markdownText(p)}})
		return b, nil, err
	case "slack":
		text := "*" + p.Title + "*\n" + p.Body
		if p.URL != "" {
			text += "\n<" + p.URL + "|查看详情>"
		}
		b, err := json.Marshal(map[string]any{"text": text})
		return b, nil, err
	default:
		b, err := json.Marshal(p)
		return b, nil, err
	}
}

// WebhookResult is the outcome of one webhook request.
type WebhookResult struct {
	OK         bool    `json:"ok"`
	Error      *string `json:"error,omitempty"`
	StatusCode *int    `json:"statusCode,omitempty"`
	LatencyMs  *int64  `json:"latencyMs,omitempty"`
}

// postWebhook sends p to target in format. Failures are returned as short
// user-facing reasons.
func (s *Service) postWebhook(ctx context.Context, target, format, secret string, allowPrivate bool, p Payload) WebhookResult {
	body, q, err := webhookBody(format, p, secret, s.now())
	if err != nil {
		return failResult(err.Error(), 0, 0)
	}
	u, err := url.Parse(target)
	if err != nil {
		return failResult("Webhook 地址无效", 0, 0)
	}
	if q != nil {
		v := u.Query()
		for k, vals := range q {
			v[k] = vals
		}
		u.RawQuery = v.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return failResult("Webhook 地址无效", 0, 0)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OmniGate-Webhook/1")
	req.Header.Set("X-OmniGate-Event", p.Type)
	if secret != "" {
		req.Header.Set(SignatureHeader, Sign(secret, body))
	}
	client := s.guarded
	if allowPrivate {
		client = s.open
	} else if s.opts.Proxy != nil && s.opts.WebhookTransport == nil {
		if err := netguard.CheckHost(ctx, u.Hostname()); err != nil {
			return failResult(describeNetErr(err), 0, 0)
		}
	}
	start := time.Now()
	resp, err := client.Do(req)
	lat := time.Since(start).Milliseconds()
	if err != nil {
		return failResult(describeNetErr(err), 0, lat)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return failResult(fmt.Sprintf("Webhook 返回 HTTP %d", resp.StatusCode), resp.StatusCode, lat)
	}
	if msg := botError(raw); msg != "" {
		return failResult("机器人接口返回错误："+msg, resp.StatusCode, lat)
	}
	code := resp.StatusCode
	return WebhookResult{OK: true, StatusCode: &code, LatencyMs: &lat}
}

// botError extracts the error of chat-bot APIs that answer HTTP 200 with an
// error code in the body (Feishu code, DingTalk / WeCom errcode).
func botError(raw []byte) string {
	var r struct {
		Code    *int   `json:"code"`
		Msg     string `json:"msg"`
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	switch {
	case r.ErrCode != nil && *r.ErrCode != 0:
		return fmt.Sprintf("%d %s", *r.ErrCode, truncate(r.ErrMsg, 200))
	case r.Code != nil && *r.Code != 0:
		return fmt.Sprintf("%d %s", *r.Code, truncate(r.Msg, 200))
	}
	return ""
}

func failResult(msg string, status int, lat int64) WebhookResult {
	r := WebhookResult{Error: &msg}
	if status != 0 {
		r.StatusCode = &status
	}
	if lat != 0 {
		r.LatencyMs = &lat
	}
	return r
}

// describeNetErr turns transport errors into short reasons without Go internals.
func describeNetErr(err error) string {
	var ne net.Error
	var dnsErr *net.DNSError
	switch {
	case errors.Is(err, netguard.ErrBlocked):
		return "目标地址被安全策略拒绝（内网/本机地址）"
	case errors.As(err, &dnsErr):
		return "无法解析 Webhook 域名"
	case errors.As(err, &ne) && ne.Timeout():
		return "请求 Webhook 超时（10 秒）"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "Webhook 服务器拒绝连接"
	case strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "x509:"):
		return "与 Webhook 服务器的 TLS 握手失败"
	default:
		return "请求 Webhook 失败"
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
