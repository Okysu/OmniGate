package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const themeScript = "\n      document.documentElement.classList.add('dark')\n    "

var indexHTML = `<!doctype html><html><head><script>` + themeScript + `</script>
<script type="module" crossorigin src="/assets/index-abc123.js"></script></head><body><div id="app"></div></body></html>`

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

func testFS(t *testing.T) fstest.MapFS {
	mod := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return fstest.MapFS{
		"index.html":                     {Data: []byte(indexHTML), ModTime: mod},
		"favicon.svg":                    {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), ModTime: mod},
		"assets/index-abc123.js":         {Data: []byte(`console.log("app")`), ModTime: mod},
		"assets/index-abc123.js.gz":      {Data: gz(t, `console.log("app")`), ModTime: mod},
		"assets/index-def456.css":        {Data: []byte(`body{}`), ModTime: mod},
		"assets/codicon-0123.ttf":        {Data: []byte{0, 1, 0, 0}, ModTime: mod},
		"assets/editor.worker-77aa.js":   {Data: []byte(`self.onmessage=()=>{}`), ModTime: mod},
		".gitkeep":                       {Data: nil, ModTime: mod},
		"docs/guide/index.html":          {Data: []byte(`<p>x</p>`), ModTime: mod},
		"assets/very/deep/chunk-9f9f.js": {Data: []byte(`1`), ModTime: mod},
	}
}

func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"error":{"code":"not_found"}}`)
}

func do(h http.Handler, method, target string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFallbackRules(t *testing.T) {
	h := newHandler(testFS(t), SourceEmbedded, http.HandlerFunc(apiNotFound))
	cases := []struct {
		method, path string
		status       int
		want         string // "index", "json404", "404", "405" or a body substring
	}{
		{"GET", "/", 200, "index"},
		{"GET", "/index.html", 200, "index"},
		{"HEAD", "/", 200, ""},
		{"GET", "/login", 200, "index"},
		{"GET", "/console", 200, "index"},
		{"GET", "/console/channels", 200, "index"},
		{"GET", "/console/channels/0b9a2d3c-1111-4f5e-9a1b-123456789abc", 200, "index"},
		{"GET", "/console/plugins/deepseek?tab=files#x", 200, "index"},
		{"GET", "/v1beta/whatever", 200, "index"}, // only whole segments are reserved
		{"GET", "/apix", 200, "index"},
		{"GET", "/docs/guide", 200, "index"}, // directories are not listed
		{"GET", "/favicon.svg", 200, "<svg"},
		{"GET", "/assets/index-abc123.js", 200, `console.log("app")`},
		{"GET", "/assets/very/deep/chunk-9f9f.js", 200, "1"},
		{"GET", "/assets/index-missing.js", 404, "404"},
		{"GET", "/assets/", 404, "404"},
		{"GET", "/robots.txt", 404, "404"},
		{"GET", "/old-logo.png", 404, "404"},
		{"GET", "/.gitkeep", 404, "404"},
		{"GET", "/assets/index-abc123.js.gz", 404, "404"},
		{"GET", "/api", 404, "json404"},
		{"GET", "/api/nope", 404, "json404"},
		{"POST", "/api/nope", 404, "json404"},
		{"GET", "/v1", 404, "json404"},
		{"GET", "/v1/nope", 404, "json404"},
		{"GET", "/healthz/", 404, "json404"},
		{"GET", "/readyz/extra", 404, "json404"},
		{"GET", "/metrics", 404, "json404"},
		{"GET", "/a/../api/x", 404, "json404"},
		{"POST", "/console/channels", 405, "405"},
		{"DELETE", "/", 405, "405"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec := do(h, c.method, c.path)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, c.status, rec.Body.String())
			}
			body := rec.Body.String()
			switch c.want {
			case "index":
				if !strings.Contains(body, `<div id="app">`) {
					t.Fatalf("want index.html, got %q", body)
				}
				if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
					t.Fatalf("content-type = %q", ct)
				}
			case "json404":
				if ct := rec.Header().Get("Content-Type"); ct != "application/json" || !strings.Contains(body, "not_found") {
					t.Fatalf("want JSON 404, got %q %q", ct, body)
				}
			case "405":
				if rec.Header().Get("Allow") != "GET, HEAD" {
					t.Fatalf("Allow = %q", rec.Header().Get("Allow"))
				}
			case "":
				if c.method == "HEAD" && body != "" {
					t.Fatalf("HEAD returned a body")
				}
			default:
				if !strings.Contains(body, c.want) {
					t.Fatalf("body %q does not contain %q", body, c.want)
				}
				if c.status == 404 && strings.Contains(body, "<div id=\"app\">") {
					t.Fatalf("404 must not return index.html")
				}
			}
		})
	}
}

func TestHeaders(t *testing.T) {
	h := newHandler(testFS(t), SourceEmbedded, nil)

	t.Run("hashed assets are immutable", func(t *testing.T) {
		rec := do(h, "GET", "/assets/index-def456.css")
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Fatalf("Cache-Control = %q", got)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
			t.Fatalf("Content-Type = %q", got)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing nosniff")
		}
		if rec.Header().Get("Content-Security-Policy") != "" {
			t.Fatal("CSP must only be sent with documents (workers would inherit it)")
		}
	})

	t.Run("content types", func(t *testing.T) {
		for p, want := range map[string]string{
			"/assets/index-abc123.js":       "text/javascript; charset=utf-8",
			"/assets/editor.worker-77aa.js": "text/javascript; charset=utf-8",
			"/assets/codicon-0123.ttf":      "font/ttf",
			"/favicon.svg":                  "image/svg+xml",
		} {
			if got := do(h, "GET", p).Header().Get("Content-Type"); got != want {
				t.Errorf("%s: Content-Type = %q, want %q", p, got, want)
			}
		}
	})

	t.Run("index is revalidated and carries security headers", func(t *testing.T) {
		for _, p := range []string{"/", "/console/channels"} {
			rec := do(h, "GET", p)
			hdr := rec.Header()
			if hdr.Get("Cache-Control") != "no-cache" {
				t.Fatalf("%s: Cache-Control = %q", p, hdr.Get("Cache-Control"))
			}
			if hdr.Get("X-Frame-Options") != "DENY" || hdr.Get("Referrer-Policy") == "" || hdr.Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("%s: missing security headers: %v", p, hdr)
			}
			csp := hdr.Get("Content-Security-Policy")
			sum := sha256.Sum256([]byte(themeScript))
			for _, want := range []string{
				"default-src 'self'",
				"script-src 'self' 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'",
				"connect-src 'self'",
				"worker-src 'self' blob:",
				"img-src 'self' data:",
				"frame-ancestors 'none'",
			} {
				if !strings.Contains(csp, want) {
					t.Fatalf("CSP %q lacks %q", csp, want)
				}
			}
			if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
				t.Fatal("inline scripts must be allowed by hash only")
			}
		}
	})

	t.Run("etag revalidation", func(t *testing.T) {
		for _, p := range []string{"/", "/assets/index-abc123.js"} {
			etag := do(h, "GET", p).Header().Get("ETag")
			if etag == "" {
				t.Fatalf("%s: no ETag", p)
			}
			if rec := do(h, "GET", p, "If-None-Match", etag); rec.Code != http.StatusNotModified {
				t.Fatalf("%s: conditional GET = %d", p, rec.Code)
			}
		}
	})

	t.Run("precompressed variant", func(t *testing.T) {
		rec := do(h, "GET", "/assets/index-abc123.js", "Accept-Encoding", "br;q=0, gzip, deflate")
		if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatalf("headers = %v", rec.Header())
		}
		if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
			t.Fatalf("Content-Length = %q, body %d bytes", rec.Header().Get("Content-Length"), rec.Body.Len())
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := io.ReadAll(zr); string(b) != `console.log("app")` {
			t.Fatalf("decoded body = %q", b)
		}
		plain := do(h, "GET", "/assets/index-abc123.js", "Accept-Encoding", "gzip;q=0")
		if plain.Header().Get("Content-Encoding") != "" || plain.Body.String() != `console.log("app")` {
			t.Fatalf("identity response = %v %q", plain.Header(), plain.Body.String())
		}
		if plain.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatal("identity response must still vary on Accept-Encoding")
		}
		if a, b := rec.Header().Get("ETag"), plain.Header().Get("ETag"); a == b {
			t.Fatalf("encoded and identity responses share ETag %s", a)
		}
	})
}

func TestPlaceholder(t *testing.T) {
	h := newHandler(fstest.MapFS{".gitkeep": {}}, SourcePlaceholder, nil)
	for _, p := range []string{"/", "/console/channels"} {
		rec := do(h, "GET", p)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "前端未构建") {
			t.Fatalf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
	if rec := do(h, "GET", "/v1/models"); rec.Code != 404 || !strings.Contains(rec.Body.String(), "not_found") {
		t.Fatalf("/v1/models: %d %q", rec.Code, rec.Body.String())
	}
}

func TestNewFromDir(t *testing.T) {
	if _, err := New(Options{Dir: t.TempDir()}); err == nil {
		t.Fatal("dir without index.html must be rejected")
	}
	if _, err := New(Options{Dir: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing dir must be rejected")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<p>v1</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if h.Source() != SourceDir {
		t.Fatalf("source = %s", h.Source())
	}
	if body := do(h, "GET", "/x").Body.String(); body != "<p>v1</p>" {
		t.Fatalf("body = %q", body)
	}
	// A replaced build on disk is picked up without restart.
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<p>v2 longer</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if body := do(h, "GET", "/x").Body.String(); body != "<p>v2 longer</p>" {
		t.Fatalf("body after update = %q", body)
	}
}

func TestEmbeddedBuildLoads(t *testing.T) {
	h, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(h, "GET", "/")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("source %s: GET / = %d %q", h.Source(), rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestAcceptsEncoding(t *testing.T) {
	for _, c := range []struct {
		header, token string
		want          bool
	}{
		{"gzip, deflate, br", "br", true},
		{"gzip;q=0.5", "gzip", true},
		{"GZIP", "gzip", true},
		{"gzip;q=0", "gzip", false},
		{"br; q=0.0", "br", false},
		{"deflate", "gzip", false},
		{"", "gzip", false},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", c.header)
		if got := acceptsEncoding(req, c.token); got != c.want {
			t.Errorf("acceptsEncoding(%q, %q) = %v", c.header, c.token, got)
		}
	}
}
