package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"omnigate/internal/platform/httpx"
	"omnigate/internal/protocol"
)

// Responses API WebSocket mode (docs/contracts/phase16-api.md): a client
// opens one connection to GET /v1/responses and sends a `response.create`
// event per turn; the server answers with the same `response.*` events the
// HTTP streaming API emits, one event per text frame.
//
// Every response.create runs through the regular HTTP pipeline as a
// streaming POST /v1/responses, so routing, protocol conversion (Chat /
// Messages upstreams), billing, quotas, request logs and the breaker apply
// unchanged. On top of that the connection keeps OpenAI's connection-local
// response cache itself: previous_response_id naming a response of this
// connection is expanded into the full input (that response's input plus
// its output), so chained turns work with store=false and on upstreams
// that keep no state at all.

const (
	wsMaxInFlight    = 16               // active responses per connection
	wsMaxLanes       = 32               // distinct named stream_id values
	wsConnLifetime   = 60 * time.Minute // like OpenAI: reconnect after an hour
	wsCachedPerLane  = 4                // responses kept per lane for chaining
	wsMaxCachedTotal = 64
)

type wsSession struct {
	g      *Gateway
	conn   *websocket.Conn
	ctx    context.Context
	header http.Header // handshake headers forwarded to every response
	base   *http.Request

	writeMu sync.Mutex
	sem     chan struct{}

	mu     sync.Mutex
	lanes  map[string]chan wsJob
	cache  map[string][]json.RawMessage // response id → full input of the next turn
	order  []string                     // cache ids, oldest first
	byLane map[string][]string
	wg     sync.WaitGroup
}

type wsJob struct {
	lane  string
	event map[string]json.RawMessage
}

// isWebSocketUpgrade reports whether r asks for a WebSocket.
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// responsesWS serves GET /v1/responses.
func (g *Gateway) responsesWS(w http.ResponseWriter, r *http.Request) {
	if !isWebSocketUpgrade(r) {
		e := protocol.NewError(protocol.ErrInvalidRequest, "GET /v1/responses requires a WebSocket upgrade (Responses WebSocket mode)")
		e.Status = http.StatusBadRequest
		writeError(w, r, protocol.OpenAIResponses, e)
		return
	}
	// Reject a bad key before upgrading, with a regular HTTP error.
	if _, gerr := g.authenticate(r); gerr != nil {
		writeError(w, r, protocol.OpenAIResponses, gerr)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return // Accept already wrote the HTTP error
	}
	conn.SetReadLimit(g.opts.MaxBodyBytes)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), wsConnLifetime)
	defer cancel()
	s := &wsSession{g: g, conn: conn, ctx: ctx, header: forwardHeaders(r.Header), base: r,
		sem: make(chan struct{}, wsMaxInFlight), lanes: map[string]chan wsJob{},
		cache: map[string][]json.RawMessage{}, byLane: map[string][]string{}}
	s.run()
	s.mu.Lock()
	for _, ch := range s.lanes {
		close(ch)
	}
	s.mu.Unlock()
	s.wg.Wait()
	if ctx.Err() != nil {
		s.sendError("", http.StatusBadRequest, "invalid_request_error", "websocket_connection_limit_reached",
			"WebSocket connections are limited to 60 minutes; open a new connection and continue.", "")
		_ = conn.Close(websocket.StatusNormalClosure, "connection limit reached")
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// forwardHeaders keeps the handshake headers a regular request would carry
// (credentials, client identification, session affinity hints).
func forwardHeaders(h http.Header) http.Header {
	out := http.Header{}
	for k, vs := range h {
		switch http.CanonicalHeaderKey(k) {
		case "Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions",
			"Sec-Websocket-Protocol", "Content-Length", "Content-Type", "Accept-Encoding":
			continue
		}
		out[k] = append([]string(nil), vs...)
	}
	out.Set("Content-Type", "application/json")
	out.Set("Accept", "text/event-stream")
	return out
}

func (s *wsSession) run() {
	for {
		_, data, err := s.conn.Read(s.ctx)
		if err != nil {
			return
		}
		var ev map[string]json.RawMessage
		if json.Unmarshal(data, &ev) != nil {
			s.sendError("", http.StatusBadRequest, "invalid_request_error", "invalid_json", "event is not a JSON object", "")
			continue
		}
		var typ string
		_ = json.Unmarshal(ev["type"], &typ)
		if typ != "response.create" {
			s.sendError("", http.StatusBadRequest, "invalid_request_error", "unknown_event_type",
				"unsupported client event type "+strconvQuote(typ)+"; only response.create is accepted", "type")
			continue
		}
		lane := ""
		if raw, ok := ev["stream_id"]; ok && !isJSONNull(raw) {
			if json.Unmarshal(raw, &lane) != nil || !validStreamID(lane) {
				s.sendError("", http.StatusBadRequest, "invalid_request_error", "invalid_stream_id",
					"stream_id must be 1–256 letters, digits, '_', '-' or '.'", "stream_id")
				continue
			}
		}
		if !s.enqueue(wsJob{lane: lane, event: ev}) {
			s.sendError(lane, http.StatusBadRequest, "invalid_request_error", "websocket_stream_limit_reached",
				"this connection already uses 32 distinct stream_id values; reuse one or open a new connection", "stream_id")
		}
	}
}

func validStreamID(s string) bool {
	if len(s) < 1 || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// enqueue appends a job to its lane (FIFO per lane; lanes run concurrently).
// A full lane queue blocks the reader (back-pressure); the lock is released
// first because lane workers take it to cache completed responses.
func (s *wsSession) enqueue(j wsJob) bool {
	ch, ok := s.laneQueue(j.lane)
	if !ok {
		return false
	}
	select {
	case ch <- j:
		return true
	case <-s.ctx.Done():
		return true
	}
}

// laneQueue returns the queue of lane, starting its worker on first use.
func (s *wsSession) laneQueue(lane string) (chan wsJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.lanes[lane]
	if !ok {
		named := 0
		for l := range s.lanes {
			if l != "" {
				named++
			}
		}
		if lane != "" && named >= wsMaxLanes {
			return nil, false
		}
		ch = make(chan wsJob, 64)
		s.lanes[lane] = ch
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for job := range ch {
				s.process(job)
			}
		}()
	}
	return ch, true
}

// process runs one response.create.
func (s *wsSession) process(j wsJob) {
	select {
	case s.sem <- struct{}{}:
	case <-s.ctx.Done():
		return
	}
	defer func() { <-s.sem }()
	body, input, err := s.buildRequest(j)
	if err != nil {
		var ge *wsEventError
		if errors.As(err, &ge) {
			s.sendError(j.lane, ge.status, "invalid_request_error", ge.code, ge.msg, ge.param)
		}
		return
	}
	var generate = true
	if raw, ok := j.event["generate"]; ok {
		_ = json.Unmarshal(raw, &generate)
	}
	if !generate {
		s.warmUp(j, body, input)
		return
	}
	rid := httpx.NewRequestID()
	req, _ := http.NewRequestWithContext(httpx.WithRequestID(s.ctx, rid), http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header = s.header.Clone()
	req.RemoteAddr = s.base.RemoteAddr
	rw := &wsResponseWriter{s: s, lane: j.lane, header: http.Header{}, input: input}
	s.g.handle(protocol.OpenAIResponses).ServeHTTP(rw, req)
	rw.finish()
}

type wsEventError struct {
	status           int
	code, msg, param string
}

func (e *wsEventError) Error() string { return e.msg }

// buildRequest turns a response.create event into a POST /v1/responses body,
// expanding previous_response_id from the connection cache. input is the full
// input of this turn (what the next turn's history starts with).
func (s *wsSession) buildRequest(j wsJob) ([]byte, []json.RawMessage, error) {
	body := map[string]json.RawMessage{}
	for k, v := range j.event {
		switch k {
		case "type", "stream_id", "generate", "stream", "background":
			continue
		}
		body[k] = v
	}
	body["stream"] = json.RawMessage("true")
	input, err := inputItems(body["input"])
	if err != nil {
		return nil, nil, &wsEventError{status: 400, code: "invalid_input", msg: err.Error(), param: "input"}
	}
	if raw, ok := body["previous_response_id"]; ok && !isJSONNull(raw) {
		var prev string
		_ = json.Unmarshal(raw, &prev)
		s.mu.Lock()
		history, cached := s.cache[prev]
		s.mu.Unlock()
		if cached {
			input = append(append([]json.RawMessage(nil), history...), input...)
			delete(body, "previous_response_id")
		}
		// Not cached here: forwarded as is (an upstream with stored responses
		// may still resolve it).
	}
	b, _ := json.Marshal(input)
	body["input"] = b
	out, err := json.Marshal(body)
	return out, input, err
}

// inputItems normalizes `input` (a string or an item array) to items.
func inputItems(raw json.RawMessage) ([]json.RawMessage, error) {
	if isJSONNull(raw) {
		return []json.RawMessage{}, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		item, _ := json.Marshal(map[string]string{"role": "user", "content": s})
		return []json.RawMessage{item}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("input must be a string or an array of input items")
	}
	return items, nil
}

// warmUp answers generate:false without calling an upstream: the input is
// cached under a new response id that later turns can chain from.
func (s *wsSession) warmUp(j wsJob, body []byte, input []json.RawMessage) {
	var req struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &req)
	id := "resp_" + randomHex(24)
	s.remember(j.lane, id, input)
	resp := map[string]any{"id": id, "object": "response", "created_at": time.Now().Unix(), "status": "completed",
		"model": req.Model, "output": []any{}, "usage": nil}
	s.sendEvent(j.lane, map[string]any{"type": "response.created", "response": map[string]any{"id": id, "object": "response",
		"created_at": resp["created_at"], "status": "in_progress", "model": req.Model, "output": []any{}}})
	s.sendEvent(j.lane, map[string]any{"type": "response.completed", "response": resp})
}

// remember caches the input of the turn after response id (bounded per lane
// and per connection).
func (s *wsSession) remember(lane, id string, history []json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[id] = history
	s.order = append(s.order, id)
	s.byLane[lane] = append(s.byLane[lane], id)
	if ids := s.byLane[lane]; len(ids) > wsCachedPerLane {
		s.forget(ids[0])
		s.byLane[lane] = ids[1:]
	}
	for len(s.order) > wsMaxCachedTotal {
		s.forget(s.order[0])
	}
}

func (s *wsSession) forget(id string) {
	delete(s.cache, id)
	for i, v := range s.order {
		if v == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

func (s *wsSession) send(b []byte) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// Not s.ctx: the connection-limit error is sent after it expired.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.conn.Write(ctx, websocket.MessageText, b)
}

// sendEvent writes a server event, tagged with its lane.
func (s *wsSession) sendEvent(lane string, ev map[string]any) {
	if lane != "" {
		ev["stream_id"] = lane
	}
	b, _ := json.Marshal(ev)
	s.send(b)
}

func (s *wsSession) sendError(lane string, status int, typ, code, msg, param string) {
	e := map[string]any{"type": typ, "code": code, "message": msg}
	if param != "" {
		e["param"] = param
	}
	s.sendEvent(lane, map[string]any{"type": "error", "status": status, "error": e})
}

// wsResponseWriter receives the pipeline's HTTP response: SSE events are
// relayed as WebSocket messages, an error response becomes an error event.
type wsResponseWriter struct {
	s      *wsSession
	lane   string
	header http.Header
	status int
	sse    bool
	buf    bytes.Buffer
	input  []json.RawMessage
}

func (w *wsResponseWriter) Header() http.Header { return w.header }

func (w *wsResponseWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.sse = code < 300 && strings.HasPrefix(w.header.Get("Content-Type"), "text/event-stream")
}

func (w *wsResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if err := w.s.ctx.Err(); err != nil {
		return 0, err
	}
	w.buf.Write(b)
	if w.sse {
		w.drain(false)
	}
	return len(b), nil
}

func (w *wsResponseWriter) Flush() {}

// drain relays every complete SSE event in the buffer.
func (w *wsResponseWriter) drain(final bool) {
	for {
		data := w.buf.Bytes()
		i := bytes.Index(data, []byte("\n\n"))
		if i < 0 {
			if !final || len(bytes.TrimSpace(data)) == 0 {
				return
			}
			i = len(data)
		}
		block := string(data[:i])
		w.buf.Next(min(i+2, len(data)))
		var payload strings.Builder
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimRight(line, "\r")
			if v, ok := strings.CutPrefix(line, "data:"); ok {
				if payload.Len() > 0 {
					payload.WriteByte('\n')
				}
				payload.WriteString(strings.TrimPrefix(v, " "))
			}
		}
		p := strings.TrimSpace(payload.String())
		if p == "" || p == "[DONE]" {
			continue
		}
		w.relay([]byte(p))
	}
}

// relay forwards one Responses stream event and caches completed turns.
func (w *wsResponseWriter) relay(p []byte) {
	var ev map[string]json.RawMessage
	if json.Unmarshal(p, &ev) != nil {
		return
	}
	var typ string
	_ = json.Unmarshal(ev["type"], &typ)
	if typ == "response.completed" {
		var r struct {
			ID     string            `json:"id"`
			Output []json.RawMessage `json:"output"`
		}
		if json.Unmarshal(ev["response"], &r) == nil && r.ID != "" {
			w.s.remember(w.lane, r.ID, append(append([]json.RawMessage(nil), w.input...), r.Output...))
		}
	}
	if w.lane != "" {
		ev["stream_id"], _ = json.Marshal(w.lane)
	}
	b, _ := json.Marshal(ev)
	w.s.send(b)
}

// finish relays the rest of a stream, or turns an error response into an
// error event.
func (w *wsResponseWriter) finish() {
	if w.sse {
		w.drain(true)
		return
	}
	status := w.status
	if status == 0 || status < 300 {
		status = http.StatusBadGateway
	}
	var env struct {
		Error map[string]any `json:"error"`
	}
	raw, _ := io.ReadAll(&w.buf)
	if json.Unmarshal(raw, &env) != nil || env.Error == nil {
		env.Error = map[string]any{"type": "server_error", "code": "upstream_invalid_response", "message": strings.TrimSpace(string(raw))}
	}
	w.s.sendEvent(w.lane, map[string]any{"type": "error", "status": status, "error": env.Error})
}

func isJSONNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || string(t) == "null"
}

func randomHex(n int) string {
	b := make([]byte, n/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
