package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Round 6 (continued), docs/contracts/phase9-api.md §1: OpenAI audio endpoints.

const audioTokenUsage = `{"type":"tokens","input_tokens":100,"output_tokens":20,"total_tokens":120,"input_token_details":{"audio_tokens":90,"text_tokens":10}}`

// audioUpstream is an OpenAI-compatible audio API.
//
// Transcriptions / translations answer by response_format: json (token usage,
// or a duration usage when the upstream model contains "whisper"),
// verbose_json (duration, no usage), text, srt and vtt; stream=true gets
// transcript.text.delta / transcript.text.done (token usage).
//
// Speech answers binary audio in two chunks: the second one is only written
// once release is closed (or after 5 s), so a client can observe that the
// first chunk arrived on its own. stream_format=sse gets speech.audio.delta /
// speech.audio.done with usage.
type audioUpstream struct {
	srv     *httptest.Server
	fail    atomic.Bool // answer 500
	breakup atomic.Bool // speech: abort the connection after the first chunk
	hits    atomic.Int64
	chunk1  []byte
	chunk2  []byte
	release chan struct{}
	mu      sync.Mutex
	reqs    []imgReq
}

func newAudioUpstream(t *testing.T) *audioUpstream {
	u := &audioUpstream{chunk1: randomBytes(4096), chunk2: randomBytes(300 << 10), release: make(chan struct{})}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *audioUpstream) last() imgReq {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reqs[len(u.reqs)-1]
}

func (u *audioUpstream) serve(w http.ResponseWriter, r *http.Request) {
	u.hits.Add(1)
	rec := imgReq{Path: r.URL.Path, ContentType: r.Header.Get("Content-Type"), Auth: r.Header.Get("Authorization"),
		Header: r.Header.Clone(), ContentLength: r.ContentLength}
	speech := strings.HasSuffix(r.URL.Path, "/audio/speech")
	if speech {
		_ = json.NewDecoder(r.Body).Decode(&rec.JSON)
	} else {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, `{"error":{"message":"bad multipart: `+err.Error()+`"}}`, http.StatusBadRequest)
			return
		}
		rec.Fields, rec.Files = r.MultipartForm.Value, map[string][]imgFile{}
		for name, fhs := range r.MultipartForm.File {
			for _, fh := range fhs {
				f, _ := fh.Open()
				b, _ := io.ReadAll(f)
				f.Close()
				rec.Files[name] = append(rec.Files[name], imgFile{fh.Filename, fh.Header.Get("Content-Type"), b})
			}
		}
	}
	u.mu.Lock()
	u.reqs = append(u.reqs, rec)
	u.mu.Unlock()
	if u.fail.Load() {
		http.Error(w, `{"error":{"message":"audio backend down"}}`, http.StatusInternalServerError)
		return
	}
	flush := func() { w.(http.Flusher).Flush() }
	ev := func(typ, data string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
		flush()
	}
	if speech {
		if rec.JSON["stream_format"] == "sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			ev("speech.audio.delta", `{"type":"speech.audio.delta","audio":"AAEC"}`)
			ev("speech.audio.delta", `{"type":"speech.audio.delta","audio":"AwQF"}`)
			ev("speech.audio.done", `{"type":"speech.audio.done","usage":{"input_tokens":10,"output_tokens":500,"total_tokens":510}}`)
			return
		}
		ct := "audio/mpeg"
		if rec.JSON["response_format"] == "wav" {
			ct = "audio/wav"
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(u.chunk1)
		flush()
		if u.breakup.Load() {
			panic(http.ErrAbortHandler)
		}
		select {
		case <-u.release:
		case <-time.After(5 * time.Second):
		}
		_, _ = w.Write(u.chunk2)
		return
	}
	model := r.FormValue("model")
	if r.FormValue("stream") == "true" {
		w.Header().Set("Content-Type", "text/event-stream")
		ev("transcript.text.delta", `{"type":"transcript.text.delta","delta":"hel"}`)
		ev("transcript.text.delta", `{"type":"transcript.text.delta","delta":"lo"}`)
		ev("transcript.text.done", `{"type":"transcript.text.done","text":"hello","usage":`+audioTokenUsage+`}`)
		return
	}
	switch r.FormValue("response_format") {
	case "", "json":
		usage := audioTokenUsage
		if strings.Contains(model, "whisper") {
			usage = `{"type":"duration","seconds":61}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"text":"hello","usage":%s}`, usage)
	case "verbose_json":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"task":"transcribe","language":"english","duration":8.47,"text":"hello","segments":[]}`)
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "hello\n")
	case "srt":
		w.Header().Set("Content-Type", "application/x-subrip")
		fmt.Fprint(w, "1\n00:00:00,000 --> 00:00:01,000\nhello\n\n")
	case "vtt":
		w.Header().Set("Content-Type", "text/vtt")
		fmt.Fprint(w, "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhello\n\n")
	}
}

func audioFile(data []byte) mpPart {
	return mpPart{name: "file", filename: "speech.mp3", ctype: "audio/mpeg", data: data}
}

func TestAudioEndpoints(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // spooled multipart bodies land here
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newAudioUpstream(t)
	e.platformChannel(map[string]any{"name": "audio", "type": "openai", "baseUrl": up.srv.URL + "/v1",
		"models": imgModels("stt", "up-stt", "whisper", "whisper-1", "tts", "up-tts", "stt-q", "whisper-q")})
	p := e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "stt", "inputPerM": "1000",
		"audioInputPerM": "3000", "outputPerM": "2000", "perRequest": "0.001", "perMinute": "0.6"}, 201)
	if p["audioInputPerM"] != "3000" || p["audioOutputPerM"] != nil || p["perMinute"] != "0.6" || p["perMCharacters"] != "0" {
		t.Fatalf("price = %v", p)
	}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "whisper", "perMinute": "0.6", "perRequest": "0.001"}, 201)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "tts", "inputPerM": "10", "outputPerM": "1",
		"audioOutputPerM": "100", "perMCharacters": "100000"}, 201)
	_, key := e.key(e.admin, map[string]any{"name": "audio"})

	transcribe := func(t *testing.T, path string, parts ...mpPart) (*http.Response, string) {
		t.Helper()
		ct, body := mpBody(parts...)
		resp := gwPostCT(t, base, path, key, ct, bytes.NewReader(body))
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, string(raw)
	}

	t.Run("transcription json: token usage with audio details, multipart forwarded intact", func(t *testing.T) {
		audio := randomBytes(200 << 10)
		resp, raw := transcribe(t, "/v1/audio/transcriptions", audioFile(audio), field("model", "stt"), field("language", "en"),
			field("timestamp_granularities[]", "word"), field("timestamp_granularities[]", "segment"), field("include[]", "logprobs"))
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || !strings.Contains(raw, `"text":"hello"`) {
			t.Fatalf("transcription = %d %s %q", resp.StatusCode, resp.Header.Get("Content-Type"), raw)
		}
		got := up.last()
		if got.Path != "/v1/audio/transcriptions" || !strings.HasPrefix(got.ContentType, "multipart/form-data; boundary=") || got.ContentLength <= 0 ||
			got.Auth != "Bearer sk-upstream-secret-0123456789" {
			t.Fatalf("upstream request: path=%s ct=%s len=%d auth=%s", got.Path, got.ContentType, got.ContentLength, got.Auth)
		}
		if got.Fields["model"][0] != "up-stt" || got.Fields["language"][0] != "en" || strings.Join(got.Fields["timestamp_granularities[]"], ",") != "word,segment" ||
			got.Fields["include[]"][0] != "logprobs" {
			t.Fatalf("fields = %v", got.Fields)
		}
		if f := got.Files["file"]; len(f) != 1 || !bytes.Equal(f[0].Data, audio) || f[0].Name != "speech.mp3" || f[0].Type != "audio/mpeg" {
			t.Fatal("audio file not forwarded intact")
		}
		l := e.lastLog(e.admin, "model=stt")
		usage := l["usage"].(map[string]any)
		// 0.001 + 10 × 1000/M + 90 × 3000/M + 20 × 2000/M
		if l["inbound"] != "openai.audio.transcriptions" || usage["input"] != float64(100) || usage["audioInputTokens"] != float64(90) ||
			usage["output"] != float64(20) || usage["audioOutputTokens"] != float64(0) || l["audioSeconds"] != float64(0) ||
			l["usageEstimated"] != false || usage["estimated"] != false || l["charge"] != "0.321" || l["channelTier"] != "platform" {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("transcription json: duration usage billed per minute", func(t *testing.T) {
		resp, raw := transcribe(t, "/v1/audio/transcriptions", field("model", "whisper"), audioFile(randomBytes(1000)))
		if resp.StatusCode != 200 || !strings.Contains(raw, `"type":"duration"`) {
			t.Fatalf("whisper = %d %s", resp.StatusCode, raw)
		}
		if got := up.last(); got.Fields["model"][0] != "whisper-1" {
			t.Fatalf("model = %v", got.Fields["model"])
		}
		l := e.lastLog(e.admin, "model=whisper")
		// 0.001 + 61 s × 0.6 / 60
		if l["audioSeconds"] != float64(61) || l["charge"] != "0.611" || l["usageEstimated"] != false {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("transcription verbose_json: duration billed per started second", func(t *testing.T) {
		resp, raw := transcribe(t, "/v1/audio/transcriptions", field("model", "whisper"), field("response_format", "verbose_json"), audioFile(randomBytes(1000)))
		if resp.StatusCode != 200 || !strings.Contains(raw, `"duration":8.47`) {
			t.Fatalf("verbose_json = %d %s", resp.StatusCode, raw)
		}
		l := e.lastLog(e.admin, "model=whisper")
		// 0.001 + 9 s × 0.01
		if l["audioSeconds"] != float64(9) || l["charge"] != "0.091" || l["usageEstimated"] != false {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("transcription text / srt / vtt: passthrough, perRequest only and flagged estimated", func(t *testing.T) {
		for _, c := range []struct{ format, ct, body string }{
			{"text", "text/plain; charset=utf-8", "hello\n"},
			{"srt", "application/x-subrip", "1\n00:00:00,000 --> 00:00:01,000\nhello\n\n"},
			{"vtt", "text/vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhello\n\n"},
		} {
			resp, raw := transcribe(t, "/v1/audio/transcriptions", field("model", "whisper"), field("response_format", c.format), audioFile(randomBytes(100)))
			if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != c.ct || raw != c.body {
				t.Fatalf("%s = %d %s %q", c.format, resp.StatusCode, resp.Header.Get("Content-Type"), raw)
			}
			if got := up.last(); got.Fields["response_format"][0] != c.format {
				t.Fatalf("response_format = %v", got.Fields["response_format"])
			}
			l := e.lastLog(e.admin, "model=whisper")
			if l["audioSeconds"] != float64(0) || l["charge"] != "0.001" || l["usageEstimated"] != true || l["usage"].(map[string]any)["estimated"] != true {
				t.Fatalf("%s log = %v", c.format, l)
			}
		}
	})

	t.Run("transcription stream: SSE passthrough with usage from transcript.text.done", func(t *testing.T) {
		resp, raw := transcribe(t, "/v1/audio/transcriptions", field("model", "stt"), field("stream", "true"), audioFile(randomBytes(100)))
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") ||
			!strings.Contains(raw, "event: transcript.text.delta") || !strings.Contains(raw, `"type":"transcript.text.done"`) {
			t.Fatalf("stream = %d %q", resp.StatusCode, raw)
		}
		l := e.lastLog(e.admin, "model=stt")
		usage := l["usage"].(map[string]any)
		if l["stream"] != true || usage["audioInputTokens"] != float64(90) || l["charge"] != "0.321" || l["usageEstimated"] != false {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("translations", func(t *testing.T) {
		resp, raw := transcribe(t, "/v1/audio/translations", field("model", "whisper"), audioFile(randomBytes(500)), field("prompt", "glossary"))
		if resp.StatusCode != 200 || !strings.Contains(raw, `"text":"hello"`) {
			t.Fatalf("translation = %d %s", resp.StatusCode, raw)
		}
		if got := up.last(); got.Path != "/v1/audio/translations" || got.Fields["model"][0] != "whisper-1" || got.Fields["prompt"][0] != "glossary" {
			t.Fatalf("upstream got %+v", got)
		}
		l := e.lastLog(e.admin, "model=whisper")
		if l["inbound"] != "openai.audio.translations" || l["audioSeconds"] != float64(61) || l["charge"] != "0.611" {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("uploads must be multipart; the model is required", func(t *testing.T) {
		code, out, raw := readBody(gwPost(t, context.Background(), base, "/v1/audio/transcriptions", key, `{"model":"stt"}`))
		if code != 400 || out["error"] == nil {
			t.Fatalf("json upload = %d %s", code, raw)
		}
		resp, raw := transcribe(t, "/v1/audio/transcriptions", audioFile(randomBytes(10)))
		if resp.StatusCode != 400 || !strings.Contains(raw, "model") {
			t.Fatalf("no model = %d %s", resp.StatusCode, raw)
		}
	})

	t.Run("speech: binary audio streamed chunk by chunk, billed by characters", func(t *testing.T) {
		input := "你好，世界！Hello" // 11 Unicode characters
		resp := gwPost(t, context.Background(), base, "/v1/audio/speech", key, `{"model":"tts","input":"`+input+`","voice":"alloy","response_format":"wav","speed":1.25}`)
		defer resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/wav" {
			t.Fatalf("speech = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		// The first chunk arrives while the upstream still holds the second one back.
		first := make([]byte, len(up.chunk1))
		got := make(chan error, 1)
		go func() { _, err := io.ReadFull(resp.Body, first); got <- err }()
		select {
		case err := <-got:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("first audio chunk was not delivered before the upstream finished (buffered)")
		}
		close(up.release)
		rest, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, up.chunk1) || !bytes.Equal(rest, up.chunk2) {
			t.Fatal("audio bytes differ")
		}
		req := up.last()
		if req.Path != "/v1/audio/speech" || req.JSON["model"] != "up-tts" || req.JSON["input"] != input || req.JSON["voice"] != "alloy" || req.JSON["speed"] != 1.25 {
			t.Fatalf("upstream got %+v", req.JSON)
		}
		l := e.lastLog(e.admin, "model=tts")
		usage := l["usage"].(map[string]any)
		// 11 characters × 100000 / M
		if l["inbound"] != "openai.audio.speech" || usage["inputCharacters"] != float64(11) || usage["input"] != float64(0) || l["charge"] != "1.1" ||
			l["usageEstimated"] != false || l["stream"] != false || l["statusCode"] != float64(200) || l["errorClass"] != nil {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("speech: SSE passthrough with usage from speech.audio.done", func(t *testing.T) {
		resp := gwPost(t, context.Background(), base, "/v1/audio/speech", key, `{"model":"tts","input":"hi","voice":"alloy","stream_format":"sse"}`)
		code, _, raw := readBody(resp)
		if code != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || strings.Count(raw, "event: speech.audio.delta") != 2 ||
			!strings.Contains(raw, `"type":"speech.audio.done"`) {
			t.Fatalf("speech sse = %d %q", code, raw)
		}
		l := e.lastLog(e.admin, "model=tts")
		usage := l["usage"].(map[string]any)
		// 10 input × 10/M + 500 audio output × 100/M (audioOutputPerM, not outputPerM)
		if l["stream"] != true || usage["input"] != float64(10) || usage["output"] != float64(500) || usage["audioOutputTokens"] != float64(500) ||
			usage["inputCharacters"] != float64(0) || l["charge"] != "0.0501" {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("audio_seconds meter in plan quotas", func(t *testing.T) {
		plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
			"name": "听写套餐", "description": "", "duration": "30d", "models": []string{"stt-q"}, "stackable": false,
			"rules": []map[string]any{{"id": "secs", "label": "每日音频秒数", "meter": "audio_seconds", "window": map[string]any{"kind": "calendar", "unit": "day"},
				"limit": "100"}},
		}, 201)
		e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": e.userID(e.admin), "planId": plan["id"], "periods": 1}, 201)
		for i := 0; i < 2; i++ {
			if resp, raw := transcribe(t, "/v1/audio/transcriptions", field("model", "stt-q"), audioFile(randomBytes(10))); resp.StatusCode != 200 {
				t.Fatalf("covered request %d = %d %s", i, resp.StatusCode, raw)
			}
			e.app.FlushLogs(context.Background())
		}
		resp, raw := transcribe(t, "/v1/audio/transcriptions", field("model", "stt-q"), audioFile(randomBytes(10)))
		if resp.StatusCode != 429 || !strings.Contains(raw, "quota_exceeded") {
			t.Fatalf("over audio quota = %d %s", resp.StatusCode, raw)
		}
		subs := e.mustDo(e.admin, http.MethodGet, "/api/billing/subscriptions", nil, 200)
		rule := subs["items"].([]any)[0].(map[string]any)["rules"].([]any)[0].(map[string]any)
		if rule["meter"] != "audio_seconds" || rule["used"] != "122" || rule["exceeded"] != true {
			t.Fatalf("rule usage = %v", rule)
		}
		l := e.lastLog(e.admin, "model=stt-q&status=success")
		if l["subscriptionId"] == nil || l["audioSeconds"] != float64(61) || l["charge"] != "0" {
			t.Fatalf("covered log = %v", l)
		}
		// Invalid meter names are still rejected.
		e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{"name": "x", "description": "", "duration": "30d", "models": []string{},
			"rules": []map[string]any{{"id": "x", "meter": "audio_minutes", "window": map[string]any{"kind": "lifetime"}, "limit": "1"}}}, 422)
	})

	t.Run("plaza: audio capabilities add the openai.audio protocol and audio prices", func(t *testing.T) {
		e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/stt", map[string]any{"capabilities": map[string]any{"audioInput": true}}, 201)
		info := e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/tts", map[string]any{"capabilities": map[string]any{"audioOutput": true}}, 201)
		if c := info["capabilities"].(map[string]any); c["audioOutput"] != true || c["audioInput"] != false {
			t.Fatalf("model info = %v", info)
		}
		e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/whisper", map[string]any{"capabilities": map[string]any{"audio": true}}, 422)
		mine := e.mustDo(e.admin, http.MethodGet, "/api/plaza/mine", nil, 200)
		entries := map[string]map[string]any{}
		for _, it := range mine["items"].([]any) {
			m := it.(map[string]any)
			entries[m["model"].(string)] = m
		}
		for _, name := range []string{"stt", "tts"} {
			if !strings.Contains(fmt.Sprint(entries[name]["protocols"]), "openai.audio") {
				t.Fatalf("plaza %s = %v", name, entries[name])
			}
		}
		if strings.Contains(fmt.Sprint(entries["whisper"]["protocols"]), "openai.audio") {
			t.Fatalf("whisper without audio capability = %v", entries["whisper"])
		}
		sp, tp := entries["stt"]["price"].(map[string]any), entries["tts"]["price"].(map[string]any)
		if sp["audioInputPerM"] != "3000" || sp["audioOutputPerM"] != nil || sp["perMinute"] != "0.6" || sp["perMCharacters"] != nil ||
			tp["audioOutputPerM"] != "100" || tp["perMCharacters"] != "100000" || tp["perMinute"] != nil {
			t.Fatalf("plaza prices: stt=%v tts=%v", sp, tp)
		}
	})

	t.Run("route preview accepts the audio inbounds", func(t *testing.T) {
		for _, inbound := range []string{"openai.audio.transcriptions", "openai.audio.speech"} {
			pv := e.mustDo(e.admin, http.MethodPost, "/api/admin/routes/preview", map[string]any{"model": "stt", "inbound": inbound}, 200)
			c := pv["candidates"].([]any)
			if len(c) != 1 || c[0].(map[string]any)["upstreamDialect"] != inbound {
				t.Fatalf("preview %s = %v", inbound, pv)
			}
		}
	})

	t.Run("body limit and spool cleanup", func(t *testing.T) {
		before := up.hits.Load()
		resp, _ := transcribe(t, "/v1/audio/transcriptions", field("model", "stt"), audioFile(make([]byte, 64<<20)))
		if resp.StatusCode != http.StatusRequestEntityTooLarge || up.hits.Load() != before {
			t.Fatalf("oversized = %d (upstream hits %d → %d)", resp.StatusCode, before, up.hits.Load())
		}
		big := randomBytes(9 << 20)
		if resp, raw := transcribe(t, "/v1/audio/transcriptions", field("model", "stt"), audioFile(big)); resp.StatusCode != 200 {
			t.Fatalf("big upload = %d %s", resp.StatusCode, raw)
		}
		if f := up.last().Files["file"]; len(f) != 1 || !bytes.Equal(f[0].Data, big) {
			t.Fatal("large audio not forwarded intact")
		}
		left, _ := filepath.Glob(filepath.Join(os.TempDir(), "omnigate-audio-*"))
		if len(left) != 0 {
			t.Fatalf("temporary files left: %v", left)
		}
	})
}

func TestAudioRoutingRetryAndBilling(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	bad, good := newAudioUpstream(t), newAudioUpstream(t)
	close(bad.release)
	close(good.release)
	bad.fail.Store(true)
	e.platformChannel(map[string]any{"name": "bad", "type": "openai", "baseUrl": bad.srv.URL + "/v1", "priority": 10,
		"models": imgModels("whisper-r", "bad-whisper", "tts-r", "bad-tts")})
	e.platformChannel(map[string]any{"name": "good", "type": "openai", "baseUrl": good.srv.URL + "/v1",
		"models": imgModels("whisper-r", "good-whisper", "tts-r", "good-tts")})
	_, key := e.key(e.admin, map[string]any{"name": "k"})

	t.Run("retry on another channel after 5xx replays the multipart body", func(t *testing.T) {
		audio := randomBytes(256 << 10)
		ct, body := mpBody(field("model", "whisper-r"), field("prompt", "retry me"), audioFile(audio))
		code, _, raw := readBody(gwPostCT(t, base, "/v1/audio/transcriptions", key, ct, bytes.NewReader(body)))
		if code != 200 {
			t.Fatalf("transcription with retry = %d %s", code, raw)
		}
		b, g := bad.last(), good.last()
		if b.Fields["model"][0] != "bad-whisper" || g.Fields["model"][0] != "good-whisper" {
			t.Fatalf("models: bad=%v good=%v", b.Fields["model"], g.Fields["model"])
		}
		for _, r := range []imgReq{b, g} {
			if f := r.Files["file"]; len(f) != 1 || !bytes.Equal(f[0].Data, audio) || r.Fields["prompt"][0] != "retry me" {
				t.Fatal("replayed body differs")
			}
		}
		l := e.lastLog(e.admin, "model=whisper-r")
		if l["attempts"] != float64(2) || l["statusCode"] != float64(200) || l["audioSeconds"] != float64(61) {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("speech retries before the first byte", func(t *testing.T) {
		resp := gwPost(t, context.Background(), base, "/v1/audio/speech", key, `{"model":"tts-r","input":"hello","voice":"alloy"}`)
		code, _, raw := readBody(resp)
		if code != 200 || raw != string(good.chunk1)+string(good.chunk2) || resp.Header.Get("Content-Type") != "audio/mpeg" {
			t.Fatalf("speech with retry = %d (%d bytes)", code, len(raw))
		}
		if bad.last().JSON["model"] != "bad-tts" || good.last().JSON["model"] != "good-tts" {
			t.Fatal("speech was not retried with the second channel's model")
		}
		if l := e.lastLog(e.admin, "model=tts-r"); l["attempts"] != float64(2) || l["statusCode"] != float64(200) {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("no retry once audio bytes reached the client", func(t *testing.T) {
		bad.fail.Store(false)
		bad.breakup.Store(true)
		defer func() { bad.fail.Store(true); bad.breakup.Store(false) }()
		goodHits := good.hits.Load()
		resp := gwPost(t, context.Background(), base, "/v1/audio/speech", key, `{"model":"tts-r","input":"hello","voice":"alloy"}`)
		defer resp.Body.Close()
		got, err := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || err == nil || !bytes.Equal(got, bad.chunk1) {
			t.Fatalf("broken speech = %d err=%v (%d bytes)", resp.StatusCode, err, len(got))
		}
		if good.hits.Load() != goodHits {
			t.Fatal("a committed audio response was retried")
		}
		l := e.lastLog(e.admin, "model=tts-r")
		if l["attempts"] != float64(1) || l["statusCode"] != float64(200) || l["errorClass"] != "upstream_invalid_response" {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("anthropic-only models are not available on audio endpoints", func(t *testing.T) {
		e.channel(e.admin, map[string]any{"name": "claude", "type": "anthropic", "baseUrl": good.srv.URL, "models": models("tts-a")})
		code, body, raw := readBody(gwPost(t, context.Background(), base, "/v1/audio/speech", key, `{"model":"tts-a","input":"hi","voice":"alloy"}`))
		if code != 404 || body["error"].(map[string]any)["code"] != "model_not_found" || !strings.Contains(raw, "OpenAI-compatible") {
			t.Fatalf("anthropic-only = %d %s", code, raw)
		}
	})

	t.Run("own channels serve audio for free", func(t *testing.T) {
		e.channel(e.admin, map[string]any{"name": "mine", "type": "openai", "baseUrl": good.srv.URL + "/v1", "models": imgModels("whisper-own", "whisper-1")})
		e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "whisper-own", "perMinute": "1", "perRequest": "1"}, 201)
		ct, body := mpBody(field("model", "whisper-own"), audioFile(randomBytes(100)))
		if code, _, raw := readBody(gwPostCT(t, base, "/v1/audio/transcriptions", key, ct, bytes.NewReader(body))); code != 200 {
			t.Fatalf("own = %d %s", code, raw)
		}
		l := e.lastLog(e.admin, "model=whisper-own")
		if l["channelTier"] != "own" || l["charge"] != "0" || l["audioSeconds"] != float64(61) {
			t.Fatalf("own log = %v", l)
		}
	})

	t.Run("group multiplier and price schedule apply; perRequest-only is flagged estimated", func(t *testing.T) {
		g := newAudioUpstream(t)
		close(g.release)
		e.platformChannel(map[string]any{"name": "glob", "type": "openai", "scope": "global", "baseUrl": g.srv.URL + "/v1",
			"models": imgModels("tts-g", "up-tts", "whisper-g", "whisper-1")})
		allDay := []map[string]any{{"days": []int{}, "start": "00:00", "end": "24:00", "multiplier": "3"}}
		e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "tts-g", "perMCharacters": "1000000", "schedule": allDay}, 201)
		e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "whisper-g", "perRequest": "0.01", "perMinute": "6"}, 201)
		half := e.newGroup(map[string]any{"name": "Half", "priceMultiplier": "0.5"})
		carolID := e.userID(e.carol)
		e.moveUser(carolID, half)
		e.enforceBilling()
		e.credit(carolID, "100")
		_, carolKey := e.key(e.carol, map[string]any{"name": "c"})
		// "héllo 世界" is 8 characters: 8 × 1 × 3 (schedule) × 0.5 (group).
		resp := gwPost(t, context.Background(), base, "/v1/audio/speech", carolKey, `{"model":"tts-g","input":"héllo 世界","voice":"alloy"}`)
		if code, _, _ := readBody(resp); code != 200 {
			t.Fatalf("speech = %d", code)
		}
		l := e.lastLog(e.carol, "model=tts-g")
		if l["charge"] != "12" || l["priceMultiplier"] != "1.5" || l["usage"].(map[string]any)["inputCharacters"] != float64(8) {
			t.Fatalf("scheduled log = %v", l)
		}
		// Text output: no usage → perRequest only (× 0.5), flagged as estimated.
		ct, body := mpBody(field("model", "whisper-g"), field("response_format", "text"), audioFile(randomBytes(100)))
		if code, _, raw := readBody(gwPostCT(t, base, "/v1/audio/transcriptions", carolKey, ct, bytes.NewReader(body))); code != 200 || raw != "hello\n" {
			t.Fatalf("text = %d %q", code, raw)
		}
		l = e.lastLog(e.carol, "model=whisper-g")
		if l["charge"] != "0.005" || l["usageEstimated"] != true || l["audioSeconds"] != float64(0) {
			t.Fatalf("estimated log = %v", l)
		}
		if w := e.mustDo(e.carol, http.MethodGet, "/api/billing/wallet", nil, 200); w["balance"] != "87.995" || w["reserved"] != "0" {
			t.Fatalf("wallet = %v", w)
		}
	})
}
