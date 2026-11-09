package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"omnigate/internal/clientdetect"
	"omnigate/internal/keys"
	"omnigate/internal/platform/httpx"
	"omnigate/internal/protocol"
	"omnigate/internal/requestlog"
)

type requestLogKey struct{}

// One state per HTTP request (WebSocket turns get independent states).
type requestLogState struct {
	entry         *requestlog.Entry
	logged        bool
	authenticated bool
}

func logState(r *http.Request) *requestLogState {
	s, _ := r.Context().Value(requestLogKey{}).(*requestLogState)
	return s
}

func newLogState(r *http.Request) *requestLogState {
	c := clientdetect.Detect(r.Header)
	e := &requestlog.Entry{ID: uuid.Must(uuid.NewV7()), StartedAt: time.Now().UTC(),
		RequestID: httpx.RequestID(r.Context()), Inbound: "gateway.http", Method: r.Method, Path: r.URL.Path,
		IPPrefix: httpx.IPPrefix(httpx.ClientIP(r.Context())), Client: &c.ID}
	if c.Version != "" {
		e.ClientVersion = &c.Version
	}
	return &requestLogState{entry: e}
}

func recordAuth(r *http.Request, a *keys.Auth) {
	if s := logState(r); s != nil {
		s.authenticated = true
		if a != nil {
			s.entry.UserID, s.entry.KeyID, s.entry.KeyName = &a.UserID, &a.Key.ID, &a.Key.Name
		}
	}
}

// RequestLogging also covers handlers that do not run the inference/billing
// pipeline, including preflight, unknown endpoints and method rejections.
// Bodies, credentials and query strings are never stored here.
func (g *Gateway) RequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A WebSocket can stay open for an hour. Wait/FlushLogs wait for
		// individual turns, not the lifetime of the connection.
		if !isWebSocketUpgrade(r) {
			g.wg.Add(1)
			defer g.wg.Done()
		}
		s := newLogState(r)
		r = r.WithContext(context.WithValue(r.Context(), requestLogKey{}, s))
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		defer func() {
			rec := recover()
			if !s.logged {
				e := s.entry
				// Routing and CORS can reject before authentication. Resolve the
				// key only for attribution, without consuming an RPM allowance.
				if !s.authenticated && presentedKey(r) != "" {
					ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
					a, err := g.keys.Authenticate(ctx, presentedKey(r))
					cancel()
					if err == nil {
						recordAuth(r, a)
					}
				}
				if e.StatusCode == 0 {
					e.StatusCode = ww.Status()
				}
				if e.StatusCode == 0 {
					e.StatusCode = http.StatusOK
				}
				if rec != nil {
					c, m := protocol.ErrInternal, "internal error"
					e.StatusCode = http.StatusInternalServerError
					if rec == http.ErrAbortHandler {
						c, m, e.StatusCode = protocol.ErrClientClosed, "connection aborted", 499
					}
					e.ErrorClass, e.ErrorMessage = &c, &m
				}
				e.DurationMs = time.Since(e.StartedAt).Milliseconds()
				g.logs.Add(e)
			}
			if rec != nil {
				panic(rec)
			}
		}()
		next.ServeHTTP(ww, r)
	})
}
