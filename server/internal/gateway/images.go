package gateway

// Image endpoints (docs/contracts/phase7-api.md §1): /v1/images/generations
// (JSON), /v1/images/edits and /v1/images/variations (multipart/form-data;
// JSON is accepted too). Requests are only routed to openai channels and passed
// through with the model rewritten.
//
// Multipart bodies are spooled once while they are parsed (to memory up to
// imageSpoolMemory, beyond that to a private temporary file in os.TempDir()
// that is removed when the request ends) so every attempt — including retries
// on another channel — can re-encode them. Each attempt streams a fresh
// encoding through an io.Pipe: parts are copied unchanged (headers and bytes),
// only the "model" field is replaced; no file is ever held in memory as a
// whole by the encoder. The encoding is deterministic, so its exact length is
// computed first and sent as Content-Length.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"omnigate/internal/protocol"
)

const (
	// defaultImageBodyBytes is the image request body limit (§1: 64 MiB).
	defaultImageBodyBytes = 64 << 20
	// imageSpoolMemory is how much of a multipart body is kept in memory
	// before it is spilled to a temporary file.
	imageSpoolMemory = 8 << 20
	// maxImageField bounds the small text fields read while parsing (prompt
	// up to 32k characters for gpt-image-*).
	maxImageField = 1 << 20
)

// spool buffers a request body for replay: memory first, then a temp file
// named after pattern (default "omnigate-image-*.part").
type spool struct {
	mu      sync.Mutex
	mem     bytes.Buffer
	file    *os.File
	size    int64
	limit   int
	pattern string
	err     error
}

func (s *spool) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	if s.file == nil && s.mem.Len()+len(p) > s.limit {
		pattern := s.pattern
		if pattern == "" {
			pattern = "omnigate-image-*.part"
		}
		f, err := os.CreateTemp("", pattern)
		if err != nil {
			s.err = fmt.Errorf("spool image request: %w", err)
			return 0, s.err
		}
		s.file = f
		if _, err := f.Write(s.mem.Bytes()); err != nil {
			s.err = fmt.Errorf("spool image request: %w", err)
			return 0, s.err
		}
		s.mem = bytes.Buffer{}
	}
	var n int
	var err error
	if s.file != nil {
		n, err = s.file.Write(p)
	} else {
		n, err = s.mem.Write(p)
	}
	s.size += int64(n)
	if err != nil {
		s.err = fmt.Errorf("spool image request: %w", err)
	}
	return n, s.err
}

// reader returns a fresh reader over the spooled bytes.
func (s *spool) reader() io.Reader {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		return io.NewSectionReader(s.file, 0, s.size)
	}
	return bytes.NewReader(s.mem.Bytes())
}

// close releases the buffer and removes the temporary file.
func (s *spool) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		name := s.file.Name()
		_ = s.file.Close()
		_ = os.Remove(name)
		s.file = nil
	}
	s.mem = bytes.Buffer{}
}

// multipartBody is a spooled multipart/form-data image request.
type multipartBody struct {
	spool    *spool
	boundary string // client boundary (parsing the spool)
	out      string // boundary of the upstream encoding (fixed per request)

	mu      sync.Mutex
	lengths map[string]int64 // model -> encoded length
}

// contentType is the Content-Type of the upstream encoding.
func (m *multipartBody) contentType() string { return "multipart/form-data; boundary=" + m.out }

// encode writes the request to w with the model field replaced. Other parts
// are copied unchanged (raw part bytes, no transfer decoding).
func (m *multipartBody) encode(w io.Writer, model string) error {
	mw := multipart.NewWriter(w)
	if err := mw.SetBoundary(m.out); err != nil {
		return err
	}
	mr := multipart.NewReader(m.spool.reader(), m.boundary)
	for {
		p, err := mr.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		h := make(textproto.MIMEHeader, len(p.Header))
		for k, v := range p.Header {
			h[k] = v
		}
		pw, err := mw.CreatePart(h)
		if err != nil {
			return err
		}
		if p.FormName() == "model" && p.FileName() == "" {
			_, err = io.WriteString(pw, model)
		} else {
			_, err = io.Copy(pw, p)
		}
		if err != nil {
			return err
		}
	}
	return mw.Close()
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// length is the exact size of encode(model) (computed once per model).
func (m *multipartBody) length(model string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n, ok := m.lengths[model]; ok {
		return n, nil
	}
	var c countWriter
	if err := m.encode(&c, model); err != nil {
		return 0, err
	}
	if m.lengths == nil {
		m.lengths = map[string]int64{}
	}
	m.lengths[model] = c.n
	return c.n, nil
}

// stream starts encoding for model into a pipe. The caller must call stop
// once the upstream request is finished (it closes the pipe and waits for
// the encoder).
func (m *multipartBody) stream(model string) (body io.ReadCloser, length int64, stop func(), err error) {
	length, err = m.length(model)
	if err != nil {
		return nil, 0, nil, err
	}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pw.CloseWithError(m.encode(pw, model))
	}()
	return pr, length, func() { _ = pr.Close(); <-done }, nil
}

func tooLarge(limit int64) *protocol.GatewayError {
	e := protocol.NewError(protocol.ErrInvalidRequest, fmt.Sprintf("request body exceeds %d bytes", limit))
	e.Status = http.StatusRequestEntityTooLarge
	return e
}

// bodyReadError maps a request body read error (limit exceeded, client gone,
// malformed body) to the gateway error.
func bodyReadError(r *http.Request, limit int64) func(err error, what string) *protocol.GatewayError {
	return func(err error, what string) *protocol.GatewayError {
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &mbe):
			return tooLarge(limit)
		case r.Context().Err() != nil:
			return protocol.NewError(protocol.ErrClientClosed, "failed to read request body")
		}
		return protocol.NewError(protocol.ErrInvalidRequest, what)
	}
}

// readImageRequest reads an image request body (JSON or multipart) within
// the image body limit and fills st.info, st.body or st.multipart.
func (g *Gateway) readImageRequest(w http.ResponseWriter, r *http.Request, st *reqState) *protocol.GatewayError {
	limit := g.opts.MaxImageBodyBytes
	body := http.MaxBytesReader(w, r.Body, limit)
	readErr := bodyReadError(r, limit)
	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "multipart/form-data" {
		raw, err := io.ReadAll(body)
		if err != nil {
			return readErr(err, "failed to read request body")
		}
		info, err := protocol.ParseImageInfo(raw)
		if err != nil {
			return convertError(err)
		}
		st.body, st.info = raw, info
		return nil
	}
	info := protocol.RequestInfo{}
	gerr := g.readMultipart(r, body, params["boundary"], st, "omnigate-image-*.part", readErr, func(name string, v []byte) {
		s := strings.TrimSpace(string(v))
		switch name {
		case "model":
			info.Model = s
		case "stream":
			info.Stream, _ = strconv.ParseBool(s)
		case "n":
			info.Images, _ = strconv.ParseInt(s, 10, 64)
		case "prompt":
			info.PromptBytes = len(v)
		}
	}, "model", "stream", "n", "prompt")
	if gerr != nil {
		return gerr
	}
	if info.Model == "" {
		return protocol.NewError(protocol.ErrInvalidRequest, "缺少 model 字段")
	}
	info.Images = protocol.ImageCount(info.Images)
	info.BodyBytes = int(st.multipart.spool.size)
	st.info = info
	return nil
}

// readMultipart spools a multipart/form-data body (already limited) into
// st.multipart (temporary file named after pattern) and passes the values of
// the small text fields listed in names to field.
func (g *Gateway) readMultipart(r *http.Request, body io.Reader, boundary string, st *reqState, pattern string,
	readErr func(error, string) *protocol.GatewayError, field func(name string, v []byte), names ...string) *protocol.GatewayError {
	if boundary == "" {
		return protocol.NewError(protocol.ErrInvalidRequest, "multipart/form-data request without a boundary")
	}
	out := multipart.NewWriter(io.Discard).Boundary()
	mb := &multipartBody{spool: &spool{limit: imageSpoolMemory, pattern: pattern}, boundary: boundary, out: out}
	st.multipart = mb
	tee := io.TeeReader(body, mb.spool)
	mr := multipart.NewReader(tee, boundary)
	for {
		p, err := mr.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if mb.spool.err != nil {
				g.log.ErrorContext(r.Context(), "multipart request spool failed", "err", mb.spool.err)
				return protocol.NewError(protocol.ErrInternal, "internal error")
			}
			return readErr(err, "malformed multipart/form-data body: "+err.Error())
		}
		name := p.FormName()
		if p.FileName() == "" && slices.Contains(names, name) {
			v, err := io.ReadAll(io.LimitReader(p, maxImageField+1))
			if err != nil {
				return readErr(err, "malformed multipart/form-data body")
			}
			if len(v) > maxImageField {
				return protocol.NewError(protocol.ErrInvalidRequest, fmt.Sprintf("form field %q is too long", name))
			}
			field(name, v)
			continue
		}
		if _, err := io.Copy(io.Discard, p); err != nil {
			return readErr(err, "malformed multipart/form-data body")
		}
	}
	// Keep the epilogue (and detect an oversized one).
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return readErr(err, "failed to read request body")
	}
	return nil
}
