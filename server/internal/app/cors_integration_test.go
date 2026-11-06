package app_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSAtAPIBoundaries(t *testing.T) {
	e := setupGateway(t)
	handler := e.app.Handler()
	request := func(method, path, origin, preflight, headers string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Origin", origin)
		if preflight != "" {
			r.Header.Set("Access-Control-Request-Method", preflight)
			r.Header.Set("Access-Control-Request-Headers", headers)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	const ui = "https://console.example.com"
	const chat = "https://chat.example.com"
	if got := request("OPTIONS", "/v1/models", chat, "GET", "authorization").Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("CORS on by default: %q", got)
	}
	e.cfg.CORSOrigins = []string{"*"}
	e.cfg.ConsoleCORSOrigins = []string{ui}
	e.cfg.AllowedOrigins = []string{chat}
	handler = e.app.Handler()
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/dashboard/billing/usage", "/dashboard/billing/subscription"} {
		w := request("OPTIONS", path, chat, "POST", "authorization,content-type,x-api-key")
		if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" || w.Header().Get("Access-Control-Allow-Headers") == "" {
			t.Fatalf("%s preflight: %d %v", path, w.Code, w.Header())
		}
	}
	if w := request("GET", "/v1/models", chat, "", ""); w.Code != 401 || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("unauthenticated gateway request: %d %v", w.Code, w.Header())
	}
	if w := request("OPTIONS", "/api/billing/lottery/draw", ui, "POST", "content-type,x-requested-with"); w.Code != 204 || w.Header().Get("Access-Control-Allow-Credentials") != "true" || w.Header().Get("Access-Control-Allow-Origin") != ui {
		t.Fatalf("console preflight: %d %v", w.Code, w.Header())
	}
	if w := request("POST", "/api/billing/lottery/draw", ui, "", ""); w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != ui {
		t.Fatalf("console CSRF: %d %v", w.Code, w.Header())
	}
	for _, path := range []string{"/api/system/info", "/healthz", "/console/benefits"} {
		if w := request("GET", path, chat, "", ""); w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("gateway wildcard leaked to %s", path)
		}
	}
}
