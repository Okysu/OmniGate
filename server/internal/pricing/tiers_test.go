package pricing

import (
	"math"
	"testing"
	"time"

	"omnigate/internal/apperr"
	"omnigate/internal/money"
	"omnigate/internal/protocol"
)

func amt(s string) *money.Amount {
	a := money.MustParse(s)
	return &a
}

func str(s string) *string { return &s }

// tieredPrice is the long-context example of phase10-api.md §1: base 10 / 1 /
// 12.5 / 50, above 272K 20 / 2 / 25 / 75.
func tieredPrice() *Price {
	m := money.MustParse
	return &Price{InputPerM: m("10"), CacheReadPM: m("1"), CacheWritePM: m("12.5"), OutputPerM: m("50"),
		Tiers: []Tier{{AboveInputTokens: 272_000, InputPerM: m("20"), OutputPerM: m("75"), CacheReadPM: amt("2"), CacheWritePM: amt("25")}}}
}

func TestTierBoundary(t *testing.T) {
	p := tieredPrice()
	for _, c := range []struct {
		u    protocol.Usage
		want string
		tier *int64
	}{
		// Exactly the threshold stays on the base prices: 272000 × 10/M + 1000 × 50/M.
		{protocol.Usage{Input: 272_000, Output: 1000}, "2.77", nil},
		// One more token: the whole request at the tier: 272001 × 20/M + 1000 × 75/M.
		{protocol.Usage{Input: 272_001, Output: 1000}, "5.51502", ptr(272_000)},
		// Cache reads count toward the threshold and are billed at the tier's
		// cache price: 1 × 20/M + 272000 × 2/M.
		{protocol.Usage{Input: 1, CacheRead: 272_000}, "0.54402", ptr(272_000)},
		// So do cache writes: 2 × 20/M + 271999 × 25/M.
		{protocol.Usage{Input: 2, CacheWrite: 271_999}, "6.800015", ptr(272_000)},
		// Below: 100 × 10/M + 1000 × 1/M + 1000 × 12.5/M + 10 × 50/M.
		{protocol.Usage{Input: 100, CacheRead: 1000, CacheWrite: 1000, Output: 10}, "0.015", nil},
		// Output tokens never count toward the threshold.
		{protocol.Usage{Input: 10, Output: 1_000_000}, "50.0001", nil},
	} {
		got, err := p.Compute(c.u, time.Time{}, One)
		if err != nil || got.String() != c.want {
			t.Errorf("Compute(%+v) = %s %v, want %s", c.u, got, err, c.want)
		}
		if at := p.AppliedTier(c.u); (at == nil) != (c.tier == nil) || (at != nil && *at != *c.tier) {
			t.Errorf("AppliedTier(%+v) = %v, want %v", c.u, at, c.tier)
		}
	}
}

func ptr(v int64) *int64 { return &v }

func TestTierSelectionMultiple(t *testing.T) {
	m := money.MustParse
	p := &Price{InputPerM: m("1"), OutputPerM: m("1"), Tiers: []Tier{
		{AboveInputTokens: 100, InputPerM: m("2"), OutputPerM: m("2")},
		{AboveInputTokens: 1000, InputPerM: m("3"), OutputPerM: m("3")},
	}}
	for prompt, want := range map[int64]string{0: "", 100: "", 101: "2", 1000: "2", 1001: "3", math.MaxInt64: "3"} {
		got := ""
		if tr := p.TierFor(prompt); tr != nil {
			got = tr.InputPerM.String()
		}
		if got != want {
			t.Errorf("TierFor(%d) = %q, want %q", prompt, got, want)
		}
	}
	if (*Price)(nil).TierFor(5) != nil || p.WithTier(nil) != p {
		t.Fatal("nil handling")
	}
	if PromptTokens(protocol.Usage{Input: math.MaxInt64, CacheRead: 5}) != math.MaxInt64 ||
		PromptTokens(protocol.Usage{Input: -5, CacheRead: 3, CacheWrite: 4}) != 7 {
		t.Fatal("PromptTokens")
	}
}

func TestTierInheritance(t *testing.T) {
	m := money.MustParse
	audioIn := m("100")
	base := &Price{InputPerM: m("10"), OutputPerM: m("50"), CacheReadPM: m("1"), CacheWritePM: m("12.5"), PerRequest: m("0.01"),
		PerImage: m("0.5"), AudioInputPM: &audioIn, PerMinute: m("6"),
		Tiers: []Tier{{AboveInputTokens: 10, InputPerM: m("20"), OutputPerM: m("75")}}}
	tp := base.WithTier(&base.Tiers[0])
	// Unset optional tier fields inherit the base version's field verbatim:
	// cache prices, the audio input price; the image input price stays unset
	// and so follows the tier's input price. Flat fees are never tiered.
	if tp.CacheReadPM.String() != "1" || tp.CacheWritePM.String() != "12.5" || tp.AudioInputPrice().String() != "100" ||
		tp.ImageInputPrice().String() != "20" || tp.AudioOutputPrice().String() != "75" || tp.PerRequest.String() != "0.01" ||
		tp.PerImage.String() != "0.5" || tp.PerMinute.String() != "6" || tp.Tiers != nil || len(base.Tiers) != 1 {
		t.Fatalf("tier prices = %+v", tp)
	}
	// 0.01 + 0.5 + 6 (60 s) + 11 × 20/M (text) + 9 × 100/M (audio) + 5 × 75/M
	u := protocol.Usage{Input: 20, AudioInput: 9, Output: 5, Images: 1, AudioSeconds: 60}
	if got, err := base.Compute(u, time.Time{}, One); err != nil || got.String() != "6.511495" {
		t.Fatalf("inherited charge = %s %v", got, err)
	}
	// Set fields override.
	base.Tiers[0].ImageInputPM, base.Tiers[0].AudioInputPM, base.Tiers[0].AudioOutputPM, base.Tiers[0].CacheReadPM =
		amt("30"), amt("200"), amt("300"), amt("0")
	tp = base.WithTier(&base.Tiers[0])
	if tp.ImageInputPrice().String() != "30" || tp.AudioInputPrice().String() != "200" || tp.AudioOutputPrice().String() != "300" ||
		tp.CacheReadPM != 0 {
		t.Fatalf("overridden tier prices = %+v", tp)
	}
}

func TestTierMultipliers(t *testing.T) {
	p := tieredPrice()
	p.ScheduleTimezone = "UTC"
	sched, err := compileSchedule([]ScheduleSlot{{Start: "00:00", End: "24:00", Multiplier: "0.8"}})
	if err != nil {
		t.Fatal(err)
	}
	p.Schedule = sched
	// (300000 × 20/M + 1000 × 75/M) × 0.8 × 0.5 = 6.075 × 0.4, one rounding.
	got, err := p.Compute(protocol.Usage{Input: 300_000, Output: 1000}, time.Now(), Multiplier(One/2))
	if err != nil || got.String() != "2.43" {
		t.Fatalf("multiplied tier charge = %s %v", got, err)
	}
	// 1 token at the tier × 0.4: 0.00002 × 0.4 rounds half away from zero.
	got, _ = p.Compute(protocol.Usage{Input: 272_001, Output: 0}, time.Now(), Multiplier(One/2))
	if got.String() != "2.176008" {
		t.Fatalf("multiplied tier charge = %s", got)
	}
}

func TestTierValidation(t *testing.T) {
	valid := func() CreateInput {
		return CreateInput{Kind: KindSell, Model: "m", InputPerM: "10", OutputPerM: "50", Tiers: []TierInput{
			{AboveInputTokens: 272_000, InputPerM: "20", OutputPerM: "75", CacheReadPerM: str("2"), CacheWritePerM: str("")}}}
	}
	p, err := Prepare(valid())
	if err != nil || len(p.Tiers) != 1 || p.Tiers[0].CacheReadPM.String() != "2" || p.Tiers[0].CacheWritePM != nil {
		t.Fatalf("valid tiers = %+v %v", p, err)
	}
	if in := p.Tiers[0].Input(); in.InputPerM != "20" || *in.CacheReadPerM != "2" || in.CacheWritePerM != nil || in.ImageInputPerM != nil {
		t.Fatalf("tier JSON = %+v", in)
	}
	if p, err := Prepare(CreateInput{Kind: KindSell, Model: "m", InputPerM: "1", Tiers: []TierInput{}}); err != nil || p.Tiers != nil {
		t.Fatalf("empty tiers = %+v %v", p, err)
	}
	six := valid()
	six.Tiers = nil
	for i := range 6 {
		six.Tiers = append(six.Tiers, TierInput{AboveInputTokens: int64(1000 * (i + 1)), InputPerM: "1", OutputPerM: "1"})
	}
	for _, c := range []struct {
		name  string
		edit  func(*CreateInput)
		field string
	}{
		{"too many", func(in *CreateInput) { in.Tiers = six.Tiers }, "tiers"},
		{"zero threshold", func(in *CreateInput) { in.Tiers[0].AboveInputTokens = 0 }, "tiers[0].aboveInputTokens"},
		{"huge threshold", func(in *CreateInput) { in.Tiers[0].AboveInputTokens = MaxTierThreshold + 1 }, "tiers[0].aboveInputTokens"},
		{"not ascending", func(in *CreateInput) {
			in.Tiers = append(in.Tiers, TierInput{AboveInputTokens: 272_000, InputPerM: "1", OutputPerM: "1"})
		}, "tiers[1].aboveInputTokens"},
		{"missing input", func(in *CreateInput) { in.Tiers[0].InputPerM = "" }, "tiers[0].inputPerM"},
		{"missing output", func(in *CreateInput) { in.Tiers[0].OutputPerM = "" }, "tiers[0].outputPerM"},
		{"negative", func(in *CreateInput) { in.Tiers[0].CacheReadPerM = str("-1") }, "tiers[0].cacheReadPerM"},
		{"too precise", func(in *CreateInput) { in.Tiers[0].AudioOutputPerM = str("0.0000000001") }, "tiers[0].audioOutputPerM"},
	} {
		in := valid()
		c.edit(&in)
		_, err := Prepare(in)
		d := validationDetails(err)
		if d == nil || d[c.field] == nil {
			t.Errorf("%s: err = %v (details %v), want %s", c.name, err, d, c.field)
		}
	}
}

func TestTierStorageAndSamePrice(t *testing.T) {
	p := tieredPrice()
	raw := encodeTiers(p.Tiers).([]byte)
	if got := decodeTiers(raw); !sameTiers(got, p.Tiers) {
		t.Fatalf("round trip = %+v (%s)", got, raw)
	}
	if encodeTiers(nil) != nil || decodeTiers(nil) != nil || decodeTiers([]byte("null")) != nil || decodeTiers([]byte(`[{"aboveInputTokens":0}]`)) != nil {
		t.Fatal("empty / invalid documents")
	}
	q := tieredPrice()
	if !SamePrice(p, q) {
		t.Fatal("identical tiers differ")
	}
	q.Tiers[0].CacheWritePM = nil
	if SamePrice(p, q) {
		t.Fatal("inherited vs explicit cache write must differ")
	}
	q = tieredPrice()
	q.Tiers[0].AboveInputTokens++
	if SamePrice(p, q) || SamePrice(p, &Price{InputPerM: p.InputPerM, OutputPerM: p.OutputPerM, CacheReadPM: p.CacheReadPM, CacheWritePM: p.CacheWritePM}) {
		t.Fatal("SamePrice ignores tiers")
	}
}

func validationDetails(err error) map[string]any {
	if err == nil {
		return nil
	}
	return apperr.As(err).Details
}

func TestFormatTokens(t *testing.T) {
	for n, want := range map[int64]string{272_000: "272K", 1_000_000: "1M", 1_500_000: "1.5M", 128_500: "128.5K", 999: "999", 1_000_001: "1000001", 1_234_567: "1234567", 1_234_000: "1234K"} {
		if got := FormatTokens(n); got != want {
			t.Errorf("FormatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}
