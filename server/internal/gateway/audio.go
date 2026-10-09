package gateway

// Audio endpoints (docs/contracts/phase9-api.md §1): /v1/audio/transcriptions
// and /v1/audio/translations (multipart/form-data, spooled and replayed like
// the image edits) and /v1/audio/speech (JSON). Requests are only routed to
// openai channels and passed through with the model rewritten; responses are
// passed through unchanged:
//
//   - transcriptions / translations: JSON, text, srt or vtt with the upstream
//     Content-Type, or SSE when stream=true;
//   - speech: binary audio copied to the client chunk by chunk as it arrives
//     (Content-Type passed through, flushed after every read, never buffered),
//     or SSE with stream_format=sse.
//
// A response is committed with its first byte: before that another channel
// may be tried, afterwards never (like streams).
//
// Metering (§1.1, in order): the usage object (tokens with audio details, or
// a duration), the verbose_json duration, the speech.audio.done usage, the
// speech input characters; otherwise only perRequest is charged and the usage
// is flagged as estimated.

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"omnigate/internal/channel"
	"omnigate/internal/protocol"
	"omnigate/internal/routing"
)

// audioChunk is the read size of the speech passthrough: every read is
// written and flushed immediately, whatever its size.
const audioChunk = 32 << 10

// readAudioRequest reads an audio request: a multipart upload within the
// upload limit (transcriptions, translations) or a JSON speech request within
// the general body limit. It fills st.info and st.body or st.multipart.
func (g *Gateway) readAudioRequest(w http.ResponseWriter, r *http.Request, st *reqState) *protocol.GatewayError {
	if st.dialect == protocol.OpenAIAudioSpeech {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, g.opts.MaxBodyBytes))
		if err != nil {
			return bodyReadError(r, g.opts.MaxBodyBytes)(err, "failed to read request body")
		}
		info, err := protocol.ParseSpeechInfo(body)
		if err != nil {
			return convertError(err)
		}
		st.body, st.info = body, info
		return nil
	}
	limit := g.opts.MaxImageBodyBytes
	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "multipart/form-data" {
		return protocol.NewError(protocol.ErrInvalidRequest, "audio uploads must be sent as multipart/form-data")
	}
	info := protocol.RequestInfo{}
	gerr := g.readMultipart(r, http.MaxBytesReader(w, r.Body, limit), params["boundary"], st, "omnigate-audio-*.part",
		bodyReadError(r, limit), func(name string, v []byte) {
			s := strings.TrimSpace(string(v))
			switch name {
			case "model":
				info.Model = s
			case "stream":
				info.Stream, _ = strconv.ParseBool(s)
			case "prompt":
				info.PromptBytes = len(v)
			}
		}, "model", "stream", "prompt")
	if gerr != nil {
		return gerr
	}
	if info.Model == "" {
		return protocol.NewError(protocol.ErrInvalidRequest, "缺少 model 字段")
	}
	info.BodyBytes = int(st.multipart.spool.size)
	st.info = info
	return nil
}

// audioEstimate is the usage reserved for an audio request before it is
// sent: the speech input characters (perMCharacters); uploads reserve
// perRequest only (the audio duration is unknown until the upstream reports it).
func audioEstimate(info protocol.RequestInfo) protocol.Usage {
	return protocol.Usage{Characters: info.Characters}
}

// audioResponse serves a successful (2xx) upstream audio response.
func (g *Gateway) audioResponse(w http.ResponseWriter, r *http.Request, st *reqState, rt *channel.Runtime, resp *http.Response,
	upDialect, upstreamModel string, ttft int64, fail failFunc) (*protocol.GatewayError, string) {
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		// The SSE passthrough of the streaming endpoints (audio processor).
		return g.stream(w, r, st, rt, resp, upDialect, upstreamModel, ttft, fail)
	case st.dialect == protocol.OpenAIAudioSpeech:
		return g.audioBinary(w, r, st, rt, resp, ct, ttft, fail)
	}
	return g.audioUnary(w, st, rt, resp, ct, ttft, fail)
}

// audioUnary passes a transcription / translation response through unchanged
// with its Content-Type (JSON, text, srt, vtt).
func (g *Gateway) audioUnary(w http.ResponseWriter, st *reqState, rt *channel.Runtime, resp *http.Response, ct string, ttft int64,
	fail failFunc) (*protocol.GatewayError, string) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, g.opts.MaxRespBytes+1))
	if err != nil {
		return fail(protocol.NewError(protocol.ErrUpstreamUnavailable, "failed to read upstream response"), routing.RetryNetwork, true)
	}
	if int64(len(raw)) > g.opts.MaxRespBytes {
		return fail(protocol.NewError(protocol.ErrUpstreamInvalid, "upstream response too large"), "", false)
	}
	usage, ok := protocol.UsageFromAudioResponse(raw)
	if !ok {
		usage = protocol.UnmeteredAudioUsage()
	}
	if ct == "" {
		ct = "text/plain; charset=utf-8"
		if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '{' {
			ct = "application/json"
		}
	}
	g.reg.Breaker.Success(rt.ID)
	g.writeHeaders(w, st, ct)
	_, _ = w.Write(raw)
	st.entry.StatusCode = http.StatusOK
	st.entry.TTFTMs = &ttft
	st.entry.Usage = usage
	return nil, ""
}

// audioBinary streams synthesized audio to the client as it arrives. Nothing
// is written before the first upstream bytes, so a failure up to then may be
// retried on another channel; afterwards the response is committed.
func (g *Gateway) audioBinary(w http.ResponseWriter, r *http.Request, st *reqState, rt *channel.Runtime, resp *http.Response, ct string,
	ttft int64, fail failFunc) (*protocol.GatewayError, string) {
	if ct == "" {
		ct = "application/octet-stream"
	}
	flusher, _ := w.(http.Flusher)
	// Abort if the upstream goes silent for StreamIdle.
	idle := time.AfterFunc(g.opts.StreamIdle, func() { resp.Body.Close() })
	defer idle.Stop()

	var firstByte *int64
	var streamErr *protocol.GatewayError
	streamClass := routing.RetryNetwork
	buf := make([]byte, audioChunk)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			idle.Reset(g.opts.StreamIdle)
			if !st.written {
				g.writeHeaders(w, st, ct)
				t := time.Since(st.start).Milliseconds()
				firstByte = &t
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				streamErr = protocol.NewError(protocol.ErrClientClosed, "client closed request")
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if errors.Is(err, io.EOF) {
			if !st.written {
				streamErr = protocol.NewError(protocol.ErrUpstreamInvalid, "upstream returned an empty audio response")
				streamClass = routing.RetryServerError
			}
			break
		}
		if err != nil {
			if r.Context().Err() != nil {
				streamErr = protocol.NewError(protocol.ErrClientClosed, "client closed request")
			} else {
				streamErr = protocol.NewError(protocol.ErrUpstreamInvalid, "upstream audio stream interrupted")
			}
			break
		}
	}
	usage := protocol.SpeechUsage(st.info.Characters)
	if streamErr != nil && !st.written {
		// Nothing reached the client: safe to try another channel.
		if streamErr.Class == protocol.ErrClientClosed {
			return fail(streamErr, "", false)
		}
		return fail(streamErr, streamClass, true)
	}
	if streamErr != nil && streamErr.Class != protocol.ErrClientClosed {
		// A binary body cannot carry an in-band error.
		g.reg.Breaker.Failure(rt.ID, streamErr.Message)
	} else {
		g.reg.Breaker.Success(rt.ID)
	}
	st.entry.StatusCode = http.StatusOK
	if streamErr != nil {
		c, m := streamErr.Class, streamErr.Message
		st.entry.ErrorClass, st.entry.ErrorMessage = &c, &m
		if c == protocol.ErrClientClosed {
			st.entry.StatusCode = 499
		}
	}
	if firstByte != nil {
		st.entry.TTFTMs = firstByte
	} else {
		st.entry.TTFTMs = &ttft
	}
	st.entry.Usage = usage
	// Abort the connection (after settlement) so the client does not mistake
	// the truncated audio for a complete file.
	st.abort = streamErr != nil && streamErr.Class != protocol.ErrClientClosed
	return nil, ""
}
