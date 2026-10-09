package pricing

import (
	"errors"
	"math"
	"testing"
	"time"

	"omnigate/internal/money"
	"omnigate/internal/protocol"
)

func TestComputeImages(t *testing.T) {
	m := money.MustParse
	imageIn := m("20")
	p := &Price{InputPerM: m("10"), OutputPerM: m("40"), PerRequest: m("0.001"), PerImage: m("0.01"), ImageInputPM: &imageIn}
	u := protocol.Usage{Input: 50, ImageInput: 30, Output: 1000, Images: 2}
	// 0.001 + 20×10/1M + 30×20/1M + 1000×40/1M + 2×0.01
	if got, err := p.Compute(u, time.Time{}, One); err != nil || got.String() != "0.0618" {
		t.Fatalf("charge = %s %v", got, err)
	}
	// imageInputPerM unset: image tokens are billed at inputPerM.
	p.ImageInputPM = nil
	if got, _ := p.Compute(u, time.Time{}, One); got.String() != "0.0615" {
		t.Fatalf("charge without imageInputPerM = %s", got)
	}
	// No usage (dall-e-*): per image and per request only.
	if got, _ := (&Price{PerImage: m("0.04"), InputPerM: m("10")}).Compute(protocol.Usage{Images: 3}, time.Time{}, One); got.String() != "0.12" {
		t.Fatalf("per-image charge = %s", got)
	}
	// Text-only requests are unchanged.
	if got, _ := (&Price{InputPerM: m("1"), OutputPerM: m("2")}).Compute(protocol.Usage{Input: 1_000_000, Output: 500_000}, time.Time{}, One); got.String() != "2" {
		t.Fatalf("text charge = %s", got)
	}
	if !SamePrice(&Price{PerImage: m("1")}, &Price{PerImage: m("1")}) || SamePrice(&Price{PerImage: m("1")}, &Price{PerImage: m("2")}) ||
		SamePrice(&Price{ImageInputPM: &imageIn}, &Price{}) {
		t.Fatal("SamePrice ignores image prices")
	}
}

func TestComputeAudio(t *testing.T) {
	m := money.MustParse
	audioIn, audioOut := m("3000"), m("100")
	p := &Price{InputPerM: m("1000"), OutputPerM: m("2000"), PerRequest: m("0.001"), AudioInputPM: &audioIn, AudioOutputPM: &audioOut,
		PerMinute: m("0.6"), PerMCharacters: m("15")}
	// 0.001 + 10 × 1000/M + 90 × 3000/M + 15 × 2000/M + 5 × 100/M
	u := protocol.Usage{Input: 100, AudioInput: 90, Output: 20, AudioOutput: 5}
	if got, err := p.Compute(u, time.Time{}, One); err != nil || got.String() != "0.3115" {
		t.Fatalf("token charge = %s %v", got, err)
	}
	// Unset audio prices fall back to inputPerM / outputPerM.
	q := *p
	q.AudioInputPM, q.AudioOutputPM = nil, nil
	if got, _ := q.Compute(u, time.Time{}, One); got.String() != "0.141" {
		t.Fatalf("fallback charge = %s", got)
	}
	// Duration: 61 s × 0.6 / 60; characters: 1000 × 15 / M.
	if got, _ := p.Compute(protocol.Usage{AudioSeconds: 61}, time.Time{}, One); got.String() != "0.611" {
		t.Fatalf("duration charge = %s", got)
	}
	if got, _ := p.Compute(protocol.Usage{Characters: 1000}, time.Time{}, One); got.String() != "0.016" {
		t.Fatalf("character charge = %s", got)
	}
	// Group multiplier applies to the total.
	half := Multiplier(One / 2)
	if got, _ := p.Compute(protocol.Usage{AudioSeconds: 60}, time.Time{}, half); got.String() != "0.3005" {
		t.Fatalf("multiplied charge = %s", got)
	}
	if SamePrice(p, &q) || SamePrice(&Price{PerMinute: m("1")}, &Price{}) || SamePrice(&Price{PerMCharacters: m("1")}, &Price{}) ||
		!SamePrice(&Price{AudioInputPM: &audioIn}, &Price{AudioInputPM: &audioIn}) {
		t.Fatal("SamePrice ignores audio prices")
	}
}

func TestComputeOverflowAndNegative(t *testing.T) {
	m := money.MustParse
	p := &Price{InputPerM: m("1"), OutputPerM: m("2000"), CacheReadPM: m("1")}
	if _, err := p.Compute(protocol.Usage{Output: math.MaxInt64}, time.Time{}, One); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("overflow err = %v", err)
	}
	if _, err := p.Compute(protocol.Usage{Output: protocol.MaxTokensLimit, Input: math.MaxInt32}, time.Time{}, Multiplier(One*1000)); err != nil {
		t.Fatalf("clamped estimate = %v", err)
	}
	// Negative counts (a misbehaving upstream) never reduce the charge.
	got, err := p.Compute(protocol.Usage{Input: 1_000_000, Output: -5_000_000, CacheRead: -1_000_000, CacheWrite: -1}, time.Time{}, One)
	if err != nil || got.String() != "1" {
		t.Fatalf("negative usage charge = %s %v", got, err)
	}
}
