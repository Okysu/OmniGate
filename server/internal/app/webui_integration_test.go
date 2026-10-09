package app_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omnigate/internal/app"
	"omnigate/internal/config"
	"omnigate/internal/platform/db"
)

// TestWebUIRouting checks the SPA mount against the real router: API paths keep
// their JSON errors, everything else falls back to index.html.
func TestWebUIRouting(t *testing.T) {
	dsn := testDB(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatal(err)
	}
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte(`<!doctype html><div id="app"></div>`), 0o644); err != nil {
		t.Fatal(err)
	}
	newServer := func(extra map[string]string) *httptest.Server {
		env := map[string]string{"OMNIGATE_DATABASE_URL": dsn, "OMNIGATE_DATA_DIR": t.TempDir(), "OMNIGATE_WEB_DIR": webDir}
		for k, v := range extra {
			env[k] = v
		}
		cfg, err := config.LoadFrom(func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		a, err := app.New(ctx, cfg, log, pool, app.Options{})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(a.Handler())
		t.Cleanup(srv.Close)
		return srv
	}
	get := func(srv *httptest.Server, method, path string) (int, http.Header, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}
	errCode := func(body string) string {
		var v struct {
			Error struct {
				Code string `json:"code"`
				Type string `json:"type"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(body), &v)
		return v.Error.Code + v.Error.Type
	}

	srv := newServer(nil)
	for _, p := range []string{"/", "/login", "/console", "/console/channels/abc"} {
		code, hdr, body := get(srv, http.MethodGet, p)
		if code != 200 || !strings.Contains(body, `<div id="app">`) || hdr.Get("Content-Security-Policy") == "" {
			t.Fatalf("GET %s = %d %q", p, code, body)
		}
	}
	if code, hdr, body := get(srv, http.MethodGet, "/api/nope"); code != 404 || !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") || errCode(body) != "not_found" {
		t.Fatalf("/api/nope = %d %q", code, body)
	}
	// Unknown data-plane endpoints keep the gateway's own OpenAI-style error (400).
	if code, _, body := get(srv, http.MethodGet, "/v1/nope"); code != 400 || errCode(body) == "" {
		t.Fatalf("/v1/nope = %d %q", code, body)
	}
	if code, _, body := get(srv, http.MethodGet, "/v1/models"); code != 401 || errCode(body) == "" {
		t.Fatalf("/v1/models = %d %q", code, body)
	}
	if code, _, body := get(srv, http.MethodGet, "/healthz/"); code != 404 || errCode(body) != "not_found" {
		t.Fatalf("/healthz/ = %d %q", code, body)
	}
	if code, _, body := get(srv, http.MethodGet, "/healthz"); code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("/healthz = %d %q", code, body)
	}
	if code, _, _ := get(srv, http.MethodPost, "/console"); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /console = %d", code)
	}

	off := newServer(map[string]string{"OMNIGATE_WEB_ENABLED": "false"})
	if code, _, body := get(off, http.MethodGet, "/console"); code != 404 || errCode(body) != "not_found" {
		t.Fatalf("web disabled: /console = %d %q", code, body)
	}

	env := map[string]string{"OMNIGATE_DATABASE_URL": dsn, "OMNIGATE_DATA_DIR": t.TempDir(), "OMNIGATE_WEB_DIR": filepath.Join(webDir, "missing")}
	cfg, err := config.LoadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.New(ctx, cfg, log, pool, app.Options{}); err == nil {
		t.Fatal("unusable OMNIGATE_WEB_DIR must fail startup")
	}
}
