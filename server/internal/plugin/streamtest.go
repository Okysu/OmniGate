package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"

	"omnigate/internal/plugin/engine"
	"omnigate/internal/protocol"
)

// streamTest collects a streaming test case's calls, events and the Chat
// Completions chunks the host would produce for them.
type streamTest struct {
	out    *TestResult
	events []json.RawMessage
	cs     *protocol.CanonicalStream
	ended  bool // an error event ended the client stream
}

func newStreamTest(out *TestResult) *streamTest {
	cs, _ := protocol.NewCanonicalStream(protocol.OpenAIChat, "test-model", true)
	out.Calls, out.ChatChunks = []StreamCall{}, []json.RawMessage{}
	return &streamTest{out: out, events: []json.RawMessage{}, cs: cs}
}

func errText(err error) string {
	var pe *engine.Error
	if errors.As(err, &pe) {
		return pe.Message
	}
	return err.Error()
}

// call records one hook call; false stops the case (the error is set).
func (st *streamTest) call(hook string, chunk *int, d time.Duration, raw json.RawMessage, err error) bool {
	c := StreamCall{Hook: hook, Chunk: chunk, DurationMs: float64(d.Microseconds()) / 1000, Events: []json.RawMessage{}}
	if chunk != nil {
		i := *chunk
		c.Chunk = &i
	}
	if err == nil && raw != nil {
		if json.Unmarshal(raw, &c.Events) != nil {
			err = &engine.Error{Kind: "output", Message: "事件必须是数组"}
		}
	}
	if err == nil {
		err = st.convert(c.Events)
	}
	if err != nil {
		msg := errText(err)
		c.Error = &msg
	}
	st.out.Calls = append(st.out.Calls, c)
	st.events = append(st.events, c.Events...)
	st.out.Output, _ = json.Marshal(st.events)
	if err != nil {
		msg := errText(err)
		st.out.Error = &msg
		return false
	}
	return true
}

// convert feeds events to the Chat converter like the gateway does.
func (st *streamTest) convert(raw []json.RawMessage) error {
	if st.ended || len(raw) == 0 {
		return nil
	}
	evs := make([]protocol.CanonicalEvent, len(raw))
	for i, r := range raw {
		if err := json.Unmarshal(r, &evs[i]); err != nil {
			return &engine.Error{Kind: "output", Message: "事件格式错误：" + err.Error()}
		}
	}
	b, err := st.cs.Events(evs)
	st.chunks(b)
	var ce *protocol.CanonicalError
	switch {
	case errors.As(err, &ce):
		// The client receives an error event and the stream ends.
		st.chunks(protocol.EncodeStreamError(protocol.OpenAIChat, protocol.NewError(protocol.ErrUpstreamUnavailable, "upstream: "+ce.Message)))
		st.ended = true
	case err != nil:
		return &engine.Error{Kind: "output", Message: "事件无效：" + err.Error()}
	}
	return nil
}

// finish closes the converted stream (finish, usage chunk) after the last call.
func (st *streamTest) finish() {
	if st.ended {
		return
	}
	b, err := st.cs.Finish()
	st.chunks(b)
	if err != nil {
		msg := "事件无效：" + err.Error()
		st.out.Error = &msg
	}
}

// chunks appends the JSON payloads of SSE bytes ([DONE] excluded).
func (st *streamTest) chunks(b []byte) {
	r := protocol.NewSSEReader(bytes.NewReader(b))
	for {
		ev, err := r.Next()
		if err == io.EOF || err != nil {
			return
		}
		d := bytes.TrimSpace(ev.Data)
		if len(d) == 0 || string(d) == "[DONE]" || !json.Valid(d) {
			continue
		}
		st.out.ChatChunks = append(st.out.ChatChunks, json.RawMessage(append([]byte(nil), d...)))
	}
}
