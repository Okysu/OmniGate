package protocol

import (
	"strings"
	"testing"
)

func TestAudioUsage(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       Usage
		ok         bool
	}{
		{"tokens with audio details", `{"text":"hi","usage":{"type":"tokens","input_tokens":100,"output_tokens":20,"total_tokens":120,
			"input_token_details":{"audio_tokens":90,"text_tokens":10}}}`, Usage{Input: 100, AudioInput: 90, Output: 20}, true},
		{"tokens without type, Responses-style details", `{"usage":{"input_tokens":30,"output_tokens":5,"input_tokens_details":{"audio_tokens":50}}}`,
			Usage{Input: 30, AudioInput: 30, Output: 5}, true},
		{"duration usage", `{"text":"hi","usage":{"type":"duration","seconds":61}}`, Usage{AudioSeconds: 61}, true},
		{"fractional duration rounds up", `{"usage":{"type":"duration","seconds":2.01}}`, Usage{AudioSeconds: 3}, true},
		{"verbose_json duration", `{"task":"transcribe","duration":8.47,"text":"hi","segments":[]}`, Usage{AudioSeconds: 9}, true},
		{"usage wins over duration", `{"duration":100,"usage":{"type":"duration","seconds":3}}`, Usage{AudioSeconds: 3}, true},
		{"json without usage", `{"text":"hi"}`, Usage{}, false},
		{"text", "hello\n", Usage{}, false},
		{"srt", "1\n00:00:00,000 --> 00:00:01,000\nhi\n", Usage{}, false},
		{"malformed", `{"text":`, Usage{}, false},
	} {
		got, ok := UsageFromAudioResponse([]byte(c.body))
		if ok != c.ok || got != c.want {
			t.Errorf("%s: usage = %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
	if AudioSeconds(8) != 8 || AudioSeconds(0) != 0 || AudioSeconds(-1) != 0 || AudioSeconds(0.2) != 1 {
		t.Fatal("AudioSeconds rounding")
	}
	if u := UnmeteredAudioUsage(); !u.Estimated || !u.IsZero() {
		t.Fatalf("unmetered = %+v", u)
	}
	if (Usage{AudioSeconds: 1}).IsZero() || (Usage{Characters: 1}).IsZero() {
		t.Fatal("audio usage counted as zero")
	}
}

func TestSpeechInfo(t *testing.T) {
	info, err := ParseSpeechInfo([]byte(`{"model":"tts-1","input":"你好，世界！Hello","voice":"alloy","stream_format":"sse"}`))
	if err != nil || info.Model != "tts-1" || !info.Stream || info.Characters != 11 {
		t.Fatalf("info = %+v %v", info, err)
	}
	if info, _ := ParseSpeechInfo([]byte(`{"model":"m","input":"a😀b","stream_format":"audio"}`)); info.Stream || info.Characters != 3 {
		t.Fatalf("binary info = %+v", info)
	}
	if _, err := ParseSpeechInfo([]byte(`{"input":"x"}`)); err == nil {
		t.Fatal("missing model accepted")
	}
	if _, err := ParseSpeechInfo([]byte(`nope`)); err == nil {
		t.Fatal("malformed body accepted")
	}
	if !IsAudio(OpenAIAudioSpeech) || !OpenAIOnly(OpenAIAudioTranslations) || IsAudioUpload(OpenAIAudioSpeech) || IsAudio(OpenAIAudio) {
		t.Fatal("audio dialect helpers")
	}
}

func TestAudioStreamProcessor(t *testing.T) {
	run := func(p StreamProcessor, stream string) string {
		t.Helper()
		r := NewSSEReader(strings.NewReader(stream))
		var out strings.Builder
		for {
			ev, err := r.Next()
			if err != nil {
				break
			}
			b, err := p.Process(ev)
			if err != nil {
				t.Fatal(err)
			}
			out.Write(b)
		}
		return out.String()
	}
	transcript := "event: transcript.text.delta\ndata: {\"type\":\"transcript.text.delta\",\"delta\":\"hi\"}\n\n" +
		"event: transcript.text.done\ndata: {\"type\":\"transcript.text.done\",\"text\":\"hi\",\"usage\":{\"type\":\"tokens\",\"input_tokens\":14,\"output_tokens\":4,\"input_token_details\":{\"audio_tokens\":12}}}\n\n"
	p := NewAudioStreamProcessor(OpenAIAudioTranscriptions, 0)
	if out := run(p, transcript); out != transcript {
		t.Fatalf("passthrough = %q", out)
	}
	if u, ok := p.Usage(); !ok || !p.Done() || u != (Usage{Input: 14, AudioInput: 12, Output: 4}) {
		t.Fatalf("transcript usage = %+v %v", u, ok)
	}
	// No usage: perRequest only, flagged.
	p = NewAudioStreamProcessor(OpenAIAudioTranscriptions, 0)
	run(p, "data: {\"type\":\"transcript.text.done\",\"text\":\"hi\"}\n\n")
	if u, _ := p.Usage(); !p.Done() || u != UnmeteredAudioUsage() {
		t.Fatalf("unmetered stream usage = %+v", u)
	}
	// Speech: usage tokens are audio output; without usage the characters count.
	p = NewAudioStreamProcessor(OpenAIAudioSpeech, 7)
	run(p, "event: speech.audio.delta\ndata: {\"type\":\"speech.audio.delta\",\"audio\":\"AA==\"}\n\n"+
		"event: speech.audio.done\ndata: {\"type\":\"speech.audio.done\",\"usage\":{\"input_tokens\":10,\"output_tokens\":500,\"total_tokens\":510}}\n\n")
	if u, _ := p.Usage(); !p.Done() || u != (Usage{Input: 10, Output: 500, AudioOutput: 500}) {
		t.Fatalf("speech usage = %+v", u)
	}
	p = NewAudioStreamProcessor(OpenAIAudioSpeech, 7)
	run(p, "data: {\"type\":\"speech.audio.done\"}\n\n")
	if u, _ := p.Usage(); u != SpeechUsage(7) {
		t.Fatalf("speech fallback usage = %+v", u)
	}
	p = NewAudioStreamProcessor(OpenAIAudioSpeech, 7)
	run(p, "data: {\"type\":\"speech.audio.delta\",\"audio\":\"AA==\"}\n\n")
	if p.Done() {
		t.Fatal("stream without a done event reported complete")
	}
}
