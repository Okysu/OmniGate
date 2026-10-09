// Package webui serves the single-page web app (web/dist) from the same port as
// the API.
//
// The build is embedded at compile time: `make build` and the Docker build copy
// web/dist into ./dist (gitignored except .gitkeep). Builds without it serve a
// placeholder page, so `go build` and `go test` never need the frontend. A build
// on disk (OMNIGATE_WEB_DIR) can replace the embedded one.
//
// Routing is deliberately route-agnostic: any GET/HEAD that is not a backend
// path and does not name an existing file gets index.html (history-API
// fallback), so the SPA owns every URL it likes (/, /login, /console/...).
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed all:dist
var embedded embed.FS

//go:embed placeholder.html
var placeholderHTML []byte

// Options configures the handler.
type Options struct {
	// Dir serves the build from this directory instead of the embedded one.
	// It must contain index.html.
	Dir string
	// NotFound answers backend paths (see IsBackendPath) that reach this handler,
	// e.g. /healthz/ or /metrics on the main port. Defaults to a JSON 404.
	NotFound http.Handler
}

// Source describes where the handler serves files from (for startup logs).
type Source string

const (
	SourceEmbedded    Source = "embedded"
	SourceDir         Source = "dir"
	SourcePlaceholder Source = "placeholder"
)

// Handler serves the SPA. Create it with New.
type Handler struct {
	fsys     fs.FS
	source   Source
	notFound http.Handler

	mu    sync.Mutex
	etags map[etagKey]string
	index *indexDoc
}

type etagKey struct {
	name string
	size int64
	mod  time.Time
}

type indexDoc struct {
	key  etagKey
	body []byte
	etag string
	csp  string
}

// New builds the handler. It fails only when Options.Dir is set but unusable.
func New(opts Options) (*Handler, error) {
	if opts.Dir != "" {
		st, err := os.Stat(opts.Dir)
		if err != nil || !st.IsDir() {
			return nil, fmt.Errorf("web dir %q is not a directory", opts.Dir)
		}
		fsys := os.DirFS(opts.Dir)
		if _, err := fs.Stat(fsys, "index.html"); err != nil {
			return nil, fmt.Errorf("web dir %q has no index.html", opts.Dir)
		}
		return newHandler(fsys, SourceDir, opts.NotFound), nil
	}
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return newHandler(sub, SourcePlaceholder, opts.NotFound), nil
	}
	return newHandler(sub, SourceEmbedded, opts.NotFound), nil
}

func newHandler(fsys fs.FS, src Source, notFound http.Handler) *Handler {
	if notFound == nil {
		notFound = http.HandlerFunc(jsonNotFound)
	}
	return &Handler{fsys: fsys, source: src, notFound: notFound, etags: map[etagKey]string{}}
}

// Source reports where files are served from.
func (h *Handler) Source() Source { return h.source }

// backendPrefixes are owned by the Go server; the SPA never falls back for them.
var backendPrefixes = []string{"/api", "/v1", "/healthz", "/readyz", "/metrics"}

// IsBackendPath reports whether p is (or is below) a backend-owned path.
func IsBackendPath(p string) bool {
	for _, pre := range backendPrefixes {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	urlPath := r.URL.Path
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}
	clean := path.Clean(urlPath)
	if IsBackendPath(clean) {
		h.notFound.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(clean, "/")
	if name == "" || name == "index.html" {
		h.serveIndex(w, r)
		return
	}
	if h.source != SourcePlaceholder && servable(name) {
		if f, st, ok := h.open(name); ok {
			defer f.Close()
			h.serveFile(w, r, name, f, st)
			return
		}
	}
	// A missing hashed asset (e.g. a chunk from the previous release) or a
	// file-looking path must 404: answered with HTML, the browser would parse it
	// as JS/CSS and fail confusingly. Dotfiles and raw .br/.gz variants 404 too.
	if name == "assets" || strings.HasPrefix(name, "assets/") || hasStaticExt(name) || !servable(name) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	h.serveIndex(w, r)
}

// servable rejects dotfiles (.gitkeep, .env…) and the precompressed variants,
// which are only served through content negotiation.
func servable(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return !strings.HasSuffix(name, ".br") && !strings.HasSuffix(name, ".gz")
}

func (h *Handler) open(name string) (fs.File, fs.FileInfo, bool) {
	f, err := h.fsys.Open(name)
	if err != nil {
		return nil, nil, false
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, false
	}
	return f, st, true
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string, f fs.File, st fs.FileInfo) {
	hdr := w.Header()
	hdr.Set("Content-Type", contentType(name))
	hdr.Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(name, "assets/") {
		// Vite puts content hashes in every file name under assets/.
		hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		hdr.Set("Cache-Control", "no-cache")
	}
	if strings.HasSuffix(name, ".html") {
		setDocumentHeaders(hdr, contentSecurityPolicy(nil))
	}

	// Precompressed variants (name.br / name.gz) produced at build time.
	served, servedSt, encoding := f, st, ""
	for _, enc := range []struct{ token, ext string }{{"br", ".br"}, {"gzip", ".gz"}} {
		vf, vst, ok := h.open(name + enc.ext)
		if !ok {
			continue
		}
		hdr.Set("Vary", "Accept-Encoding")
		if encoding == "" && acceptsEncoding(r, enc.token) {
			served, servedSt, encoding = vf, vst, enc.token
			defer vf.Close()
			continue
		}
		vf.Close()
	}
	if encoding != "" {
		hdr.Set("Content-Encoding", encoding)
		// ServeContent omits Content-Length for encoded bodies; full responses
		// can still declare it (ranges are left to ServeContent).
		if r.Header.Get("Range") == "" {
			hdr.Set("Content-Length", strconv.FormatInt(servedSt.Size(), 10))
		}
	}
	rs, err := readSeeker(served)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	if etag, err := h.etag(name+"|"+encoding, servedSt, rs); err == nil {
		hdr.Set("ETag", etag)
	}
	http.ServeContent(w, r, name, servedSt.ModTime(), rs)
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	doc, err := h.loadIndex()
	if err != nil {
		http.Error(w, "web ui unavailable", http.StatusInternalServerError)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("ETag", doc.etag)
	setDocumentHeaders(hdr, doc.csp)
	http.ServeContent(w, r, "index.html", doc.key.mod, bytes.NewReader(doc.body))
}

// loadIndex reads index.html once per version (re-read from disk when it
// changes in Dir mode) and derives the CSP for its inline scripts.
func (h *Handler) loadIndex() (*indexDoc, error) {
	key := etagKey{name: "index.html"}
	var body []byte
	if h.source == SourcePlaceholder {
		body = placeholderHTML
		key.size = int64(len(body))
	} else {
		st, err := fs.Stat(h.fsys, "index.html")
		if err != nil {
			return nil, err
		}
		key.size, key.mod = st.Size(), st.ModTime()
	}
	h.mu.Lock()
	if doc := h.index; doc != nil && doc.key == key {
		h.mu.Unlock()
		return doc, nil
	}
	h.mu.Unlock()
	if body == nil {
		var err error
		if body, err = fs.ReadFile(h.fsys, "index.html"); err != nil {
			return nil, err
		}
	}
	sum := sha256.Sum256(body)
	doc := &indexDoc{key: key, body: body, etag: `"` + hex.EncodeToString(sum[:16]) + `"`, csp: contentSecurityPolicy(body)}
	h.mu.Lock()
	h.index = doc
	h.mu.Unlock()
	return doc, nil
}

func (h *Handler) etag(id string, st fs.FileInfo, rs io.ReadSeeker) (string, error) {
	key := etagKey{name: id, size: st.Size(), mod: st.ModTime()}
	h.mu.Lock()
	tag, ok := h.etags[key]
	h.mu.Unlock()
	if ok {
		return tag, nil
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, rs); err != nil {
		return "", err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	tag = `"` + hex.EncodeToString(sum.Sum(nil)[:16]) + `"`
	h.mu.Lock()
	h.etags[key] = tag
	h.mu.Unlock()
	return tag, nil
}

func readSeeker(f fs.File) (io.ReadSeeker, error) {
	if rs, ok := f.(io.ReadSeeker); ok {
		return rs, nil
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}

func acceptsEncoding(r *http.Request, token string) bool {
	for _, v := range r.Header.Values("Accept-Encoding") {
		for _, part := range strings.Split(v, ",") {
			coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			if !strings.EqualFold(strings.TrimSpace(coding), token) {
				continue
			}
			q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
			return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
		}
	}
	return false
}

// setDocumentHeaders adds the security headers for HTML documents.
func setDocumentHeaders(hdr http.Header, csp string) {
	hdr.Set("Content-Security-Policy", csp)
	hdr.Set("X-Frame-Options", "DENY")
	hdr.Set("Referrer-Policy", "same-origin")
	hdr.Set("Cross-Origin-Opener-Policy", "same-origin")
	hdr.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
}

var inlineScript = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
var srcAttr = regexp.MustCompile(`(?i)\ssrc\s*=`)

// contentSecurityPolicy returns the policy for the SPA document. Inline
// <script> blocks in index.html (the pre-paint theme snippet) are allowed by
// hash, computed from the served file, so the policy follows frontend changes
// without 'unsafe-inline' for scripts.
//
//   - style-src 'unsafe-inline': reka-ui / vue-sonner set style attributes and
//     Monaco injects <style> elements at runtime.
//   - img-src https:: avatars come from the identity provider (GitHub, any OIDC
//     "picture" URL).
//   - worker-src blob:: Monaco may create workers from blob URLs; its regular
//     workers are same-origin files under /assets/. Workers get their own CSP
//     from their response (none), so the TypeScript worker may still eval.
func contentSecurityPolicy(index []byte) string {
	scripts := []string{"'self'"}
	for _, m := range inlineScript.FindAllSubmatch(index, -1) {
		if srcAttr.Match(m[1]) {
			continue
		}
		sum := sha256.Sum256(m[2])
		scripts = append(scripts, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + strings.Join(scripts, " "),
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob: https:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"worker-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

// contentTypes covers extensions that Go's builtin table lacks; minimal images
// (distroless) have no /etc/mime.types to fall back on.
var contentTypes = map[string]string{
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".html":        "text/html; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".svg":         "image/svg+xml",
	".ico":         "image/x-icon",
	".png":         "image/png",
	".webp":        "image/webp",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".otf":         "font/otf",
	".wasm":        "application/wasm",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
}

func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if ct, ok := contentTypes[ext]; ok {
		return ct
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// hasStaticExt reports whether the last path segment looks like a file the
// build would have produced; such paths 404 instead of falling back.
func hasStaticExt(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	if ext == "" {
		return false
	}
	if _, ok := contentTypes[ext]; ok {
		return true
	}
	switch ext {
	case ".jpg", ".jpeg", ".gif", ".avif", ".eot", ".xml", ".br", ".gz":
		return true
	}
	return false
}

func jsonNotFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"接口不存在"}}`)
}
