// Package protocol implements the client/upstream wire protocols: OpenAI Chat
// Completions, OpenAI Responses and Anthropic Messages. It contains SSE framing,
// usage extraction, error envelopes and the Chat <-> Messages converters.
//
// Phase 1 note: with only two convertible dialects, conversion is implemented
// as direct pairwise translators rather than through a canonical model (see
// docs/contracts/protocol-adapter.md §1). Same-dialect traffic is passed through
// with only the model name rewritten.
package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// MaxEventSize bounds a single SSE event; larger events are treated as malformed.
const MaxEventSize = 16 << 20

var ErrEventTooLarge = errors.New("protocol: SSE event too large")

// Event is one Server-Sent Event. Raw holds the exact bytes received (including
// the terminating blank line) so passthrough can forward them unchanged.
type Event struct {
	Name string
	Data []byte
	Raw  []byte
}

// SSEReader reads events one at a time without buffering the whole stream.
type SSEReader struct {
	r *bufio.Reader
}

func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// Next returns the next event. At end of stream it returns io.EOF; a trailing
// event without a terminating blank line is still returned first.
func (s *SSEReader) Next() (*Event, error) {
	var ev Event
	var raw bytes.Buffer
	var data [][]byte
	sawField := false
	for {
		line, err := s.readLine()
		if len(line) > 0 || err == nil {
			raw.Write(line)
		}
		if raw.Len() > MaxEventSize {
			return nil, ErrEventTooLarge
		}
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) == 0 && err == nil {
			if sawField {
				ev.Data = bytes.Join(data, []byte("\n"))
				ev.Raw = raw.Bytes()
				return &ev, nil
			}
			raw.Reset() // stray blank line
			continue
		}
		if len(trimmed) > 0 {
			sawField = true
			field, value, _ := bytes.Cut(trimmed, []byte(":"))
			value = bytes.TrimPrefix(value, []byte(" "))
			switch string(field) {
			case "event":
				ev.Name = string(value)
			case "data":
				data = append(data, append([]byte(nil), value...))
			}
			// comments (":"), id and retry are kept in Raw only
		}
		if err != nil {
			if sawField && err == io.EOF {
				ev.Data = bytes.Join(data, []byte("\n"))
				for !bytes.HasSuffix(raw.Bytes(), []byte("\n\n")) {
					raw.WriteString("\n")
				}
				ev.Raw = raw.Bytes()
				return &ev, nil
			}
			return nil, err
		}
	}
}

func (s *SSEReader) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := s.r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > MaxEventSize {
			return nil, ErrEventTooLarge
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return buf, err
	}
}

// IsComment reports whether the event only contains comments (e.g. ": ping").
func (e *Event) IsComment() bool { return e.Name == "" && len(e.Data) == 0 }

// FormatEvent encodes an SSE event.
func FormatEvent(name string, data []byte) []byte {
	var b bytes.Buffer
	if name != "" {
		b.WriteString("event: ")
		b.WriteString(name)
		b.WriteByte('\n')
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		b.WriteString("data: ")
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.Bytes()
}
