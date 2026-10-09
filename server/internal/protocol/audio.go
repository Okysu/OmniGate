package protocol

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"unicode/utf8"
)

// Audio endpoints (docs/contracts/phase9-api.md §1): requests are passed
// through to OpenAI-compatible channels with only the model rewritten;
// responses (JSON, text, subtitles, binary audio or SSE) are forwarded
// unchanged and metered from what the upstream reports (§1.1).

// Audio dialects. OpenAIAudio is the plaza protocol covering all three.
const (
	OpenAIAudio               = "openai.audio"
	OpenAIAudioTranscriptions = "openai.audio.transcriptions"
	OpenAIAudioTranslations   = "openai.audio.translations"
	OpenAIAudioSpeech         = "openai.audio.speech"
)

// IsAudio reports whether dialect is one of the audio endpoints.
func IsAudio(dialect string) bool {
	return dialect == OpenAIAudioTranscriptions || dialect == OpenAIAudioTranslations || dialect == OpenAIAudioSpeech
}

// IsAudioUpload reports whether dialect takes an uploaded audio file
// (multipart/form-data: transcriptions and translations).
func IsAudioUpload(dialect string) bool {
	return dialect == OpenAIAudioTranscriptions || dialect == OpenAIAudioTranslations
}

// ParseSpeechInfo extracts routing information from a speech request: the
// model, whether SSE was requested (stream_format: "sse") and the number of
// Unicode code points of input (billed with perMCharacters).
func ParseSpeechInfo(body []byte) (RequestInfo, error) {
	var r struct {
		Model        string          `json:"model"`
		Input        json.RawMessage `json:"input"`
		StreamFormat string          `json:"stream_format"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return RequestInfo{}, invalid("请求体不是合法的 JSON：%v", err)
	}
	if strings.TrimSpace(r.Model) == "" {
		return RequestInfo{}, invalid("缺少 model 字段")
	}
	info := RequestInfo{Model: r.Model, Stream: r.StreamFormat == "sse", BodyBytes: len(body), PromptBytes: len(r.Input)}
	var input string
	if len(r.Input) > 0 && json.Unmarshal(r.Input, &input) == nil {
		info.Characters = int64(utf8.RuneCountInString(input))
	}
	return info, nil
}

// AudioSeconds converts a reported duration to billed seconds: billing is per
// second, a started second counts as a whole one.
func AudioSeconds(d float64) int64 {
	if d <= 0 || math.IsNaN(d) || math.IsInf(d, 0) {
		return 0
	}
	if d >= math.MaxInt32 {
		return math.MaxInt32
	}
	return int64(math.Ceil(d - 1e-9))
}

type tokenDetails struct {
	AudioTokens int64 `json:"audio_tokens"`
	TextTokens  int64 `json:"text_tokens"`
}

// audioUsage is the usage object of the audio endpoints: {type: "tokens",
// input_tokens, output_tokens, input_token_details: {audio_tokens,
// text_tokens}} or {type: "duration", seconds}.
type audioUsage struct {
	Type         string   `json:"type"`
	InputTokens  *int64   `json:"input_tokens"`
	OutputTokens *int64   `json:"output_tokens"`
	Seconds      *float64 `json:"seconds"`
	// input_token_details (audio API) and input_tokens_details (Responses
	// style) are both accepted.
	InputDetails   *tokenDetails `json:"input_token_details"`
	InputDetailsS  *tokenDetails `json:"input_tokens_details"`
	OutputDetails  *tokenDetails `json:"output_token_details"`
	OutputDetailsS *tokenDetails `json:"output_tokens_details"`
}

// normalize converts the usage object; ok is false when it carries neither
// tokens nor a duration. Speech output is audio: without output details all
// output tokens are audio output tokens.
func (a *audioUsage) normalize(speech bool) (Usage, bool) {
	if a == nil {
		return Usage{}, false
	}
	if a.Type == "duration" || (a.Type == "" && a.Seconds != nil && a.InputTokens == nil && a.OutputTokens == nil) {
		if a.Seconds == nil {
			return Usage{}, false
		}
		return Usage{AudioSeconds: AudioSeconds(*a.Seconds)}, true
	}
	if a.InputTokens == nil && a.OutputTokens == nil {
		return Usage{}, false
	}
	var u Usage
	if a.InputTokens != nil {
		u.Input = max(*a.InputTokens, 0)
	}
	if a.OutputTokens != nil {
		u.Output = max(*a.OutputTokens, 0)
	}
	in, out := a.InputDetails, a.OutputDetails
	if in == nil {
		in = a.InputDetailsS
	}
	if out == nil {
		out = a.OutputDetailsS
	}
	if in != nil {
		u.AudioInput = min(max(in.AudioTokens, 0), u.Input)
	}
	switch {
	case out != nil:
		u.AudioOutput = min(max(out.AudioTokens, 0), u.Output)
	case speech:
		u.AudioOutput = u.Output
	}
	return u, true
}

// UsageFromAudioResponse meters a non-streaming transcription or translation
// response (§1.1): the usage object (tokens or duration) first, then the
// duration of verbose_json. ok is false when neither is present (text, srt
// and vtt responses carry no usage).
func UsageFromAudioResponse(body []byte) (Usage, bool) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return Usage{}, false
	}
	var r struct {
		Usage    *audioUsage `json:"usage"`
		Duration *float64    `json:"duration"`
	}
	if json.Unmarshal(body, &r) != nil {
		return Usage{}, false
	}
	if u, ok := r.Usage.normalize(false); ok {
		return u, true
	}
	if r.Duration != nil {
		return Usage{AudioSeconds: AudioSeconds(*r.Duration)}, true
	}
	return Usage{}, false
}

// SpeechUsage is the usage of a speech request without a usage report: its
// input characters (billed with perMCharacters).
func SpeechUsage(characters int64) Usage { return Usage{Characters: characters} }

// UnmeteredAudioUsage is the usage when the upstream reported nothing usable:
// only perRequest is charged and the request log says so (§1.1 item 4).
func UnmeteredAudioUsage() Usage { return Usage{Estimated: true} }

// audioPassthrough forwards audio SSE events unchanged
// (transcript.text.delta / transcript.text.done, speech.audio.delta /
// speech.audio.done) and meters the done events.
type audioPassthrough struct {
	speech     bool
	characters int64
	usage      Usage
	hasUsage   bool
	done       bool
}

// NewAudioStreamProcessor returns the stream processor of the audio
// endpoints; characters is the speech input size (fallback metering).
func NewAudioStreamProcessor(dialect string, characters int64) StreamProcessor {
	return &audioPassthrough{speech: dialect == OpenAIAudioSpeech, characters: characters}
}

func (p *audioPassthrough) Process(ev *Event) ([]byte, error) {
	data := bytes.TrimSpace(ev.Data)
	if bytes.Equal(data, doneData) {
		p.done = true
		return ev.Raw, nil
	}
	var e struct {
		Type  string      `json:"type"`
		Usage *audioUsage `json:"usage"`
	}
	if len(data) == 0 || json.Unmarshal(data, &e) != nil {
		return ev.Raw, nil
	}
	typ := e.Type
	if typ == "" {
		typ = ev.Name
	}
	switch {
	case typ == "transcript.text.done" || typ == "speech.audio.done":
		p.done = true
		if u, ok := e.Usage.normalize(p.speech); ok {
			p.usage, p.hasUsage = u, true
		}
	case typ == "error":
		p.done = true // terminal: the error reached the client in-band
	}
	return ev.Raw, nil
}

func (p *audioPassthrough) Finish() []byte { return nil }

// Usage returns the metered usage. It is always final (ok = true): the
// reported usage, else the speech input characters, else nothing but
// perRequest (flagged Estimated).
func (p *audioPassthrough) Usage() (Usage, bool) {
	switch {
	case p.hasUsage:
		return p.usage, true
	case p.speech:
		return SpeechUsage(p.characters), true
	}
	return UnmeteredAudioUsage(), true
}

func (p *audioPassthrough) OutputBytes() int { return 0 }
func (p *audioPassthrough) Done() bool       { return p.done }
