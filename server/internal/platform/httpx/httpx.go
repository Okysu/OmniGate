// Package httpx contains HTTP plumbing shared by the control plane (/api) and the
// data plane (/v1): JSON helpers, request IDs, client IP extraction, panic recovery.
package httpx

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	clientIPKey
)

const RequestIDHeader = "X-OmniGate-Request-Id"

// RequestID returns the request id stored in ctx.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// ClientIP returns the resolved client address stored in ctx.
func ClientIP(ctx context.Context) netip.Addr {
	ip, _ := ctx.Value(clientIPKey).(netip.Addr)
	return ip
}

// IPPrefix truncates an address for privacy-preserving storage (/24 IPv4, /48 IPv6).
func IPPrefix(ip netip.Addr) string {
	if !ip.IsValid() {
		return ""
	}
	ip = ip.Unmap()
	bits := 48
	if ip.Is4() {
		bits = 24
	}
	p, _ := ip.Prefix(bits)
	return p.String()
}

// NewRequestID returns a UUIDv7-based id, falling back to random bytes.
func NewRequestID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return uuid.UUID(b).String()
}

// Base installs request id, client IP resolution and panic recovery. The request
// id is always generated server-side; client-supplied ids are not trusted.
func Base(log *slog.Logger, trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := NewRequestID()
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			ctx = context.WithValue(ctx, clientIPKey, resolveClientIP(r, trusted))
			w.Header().Set(RequestIDHeader, id)
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					log.Error("panic", "request_id", id, "panic", rec, "stack", string(debug.Stack()))
					WriteError(w, r.WithContext(ctx), apperr.Internal(errors.New("panic")))
				}
			}()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func resolveClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote, _ := netip.ParseAddr(host)
	remote = remote.Unmap()
	if !isTrusted(remote, trusted) {
		return remote
	}
	// Walk X-Forwarded-For right-to-left, skipping trusted proxies.
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !isTrusted(a, trusted) {
			return a
		}
		remote = a
	}
	return remote
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// AccessLog logs one structured line per request. It never logs headers, query
// strings or bodies, which may carry credentials.
func AccessLog(log *slog.Logger, plane string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.LogAttrs(r.Context(), slog.LevelInfo, "http",
				slog.String("plane", plane),
				slog.String("request_id", RequestID(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Int64("bytes", sw.bytes),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush keeps streaming (SSE) working through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error errorPayload `json:"error"`
}
type errorPayload struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"requestId"`
	Details   map[string]any `json:"details,omitempty"`
}

// StatusFor maps an application error kind to an HTTP status.
func StatusFor(k apperr.Kind) int {
	switch k {
	case apperr.KindValidation:
		return http.StatusUnprocessableEntity
	case apperr.KindUnauthenticated:
		return http.StatusUnauthorized
	case apperr.KindForbidden:
		return http.StatusForbidden
	case apperr.KindNotFound:
		return http.StatusNotFound
	case apperr.KindConflict:
		return http.StatusConflict
	case apperr.KindRateLimited:
		return http.StatusTooManyRequests
	case apperr.KindUpstream:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// WriteError writes the control-plane error envelope. Internal causes are logged
// by the caller's logger, never returned.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	e := apperr.As(err)
	if e.Kind == apperr.KindInternal {
		slog.Default().ErrorContext(r.Context(), "internal error", "request_id", RequestID(r.Context()), "err", err)
	}
	WriteJSON(w, StatusFor(e.Kind), errorBody{Error: errorPayload{
		Code: e.Code, Message: e.Message, RequestID: RequestID(r.Context()), Details: e.Details,
	}})
}

// DecodeJSON strictly decodes a JSON body with a size limit.
func DecodeJSON(r *http.Request, v any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		return apperr.Validation("Content-Type 必须为 application/json", nil)
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apperr.Validation("请求体不是合法的 JSON："+err.Error(), nil)
	}
	return nil
}

// Page is the uniform pagination input for list endpoints.
type Page struct {
	Page     int
	PageSize int
}

func (p Page) Offset() int { return (p.Page - 1) * p.PageSize }

// ParsePage reads ?page=&pageSize= with defaults 1/20 and a max page size of 200.
func ParsePage(r *http.Request) Page {
	p := Page{Page: 1, PageSize: 20}
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
		p.Page = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("pageSize")); err == nil && v > 0 {
		p.PageSize = min(v, 200)
	}
	return p
}

// List is the uniform list response envelope.
type List[T any] struct {
	Items    []T `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}

// WithRequestID returns ctx with id as the request id (requests the server
// starts itself, e.g. one per response on a WebSocket connection).
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// Hijack lets WebSocket upgrades through the access-log wrapper.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.status = http.StatusSwitchingProtocols
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
