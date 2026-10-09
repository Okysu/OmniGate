package billing

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

var codeRe = regexp.MustCompile(`^OG-[0-9A-HJKMNP-TV-Z]{5}-[0-9A-HJKMNP-TV-Z]{5}-[0-9A-HJKMNP-TV-Z]{5}-[0-9A-HJKMNP-TV-Z]{5}$`)

func TestGenerateCodeFormat(t *testing.T) {
	seen := map[string]bool{}
	counts := map[rune]int{}
	for range 2000 {
		display, norm, err := GenerateCode()
		if err != nil {
			t.Fatal(err)
		}
		if !codeRe.MatchString(display) {
			t.Fatalf("bad format %q", display)
		}
		if len(norm) != 20 || strings.ReplaceAll(strings.TrimPrefix(display, "OG-"), "-", "") != norm {
			t.Fatalf("normalized %q does not match display %q", norm, display)
		}
		if seen[norm] {
			t.Fatalf("duplicate code %q", norm)
		}
		seen[norm] = true
		got, ok := NormalizeCode(display)
		if !ok || got != norm {
			t.Fatalf("NormalizeCode(%q) = %q, %v; want %q", display, got, ok, norm)
		}
		for _, r := range norm {
			counts[r]++
		}
	}
	// Every alphabet symbol should appear (40000 samples, 32 symbols).
	for _, r := range crockford {
		if counts[r] == 0 {
			t.Fatalf("symbol %q never generated", r)
		}
	}
	for _, r := range "ILOU" {
		if counts[r] != 0 {
			t.Fatalf("ambiguous symbol %q generated", r)
		}
	}
}

func TestNormalizeCode(t *testing.T) {
	const norm = "ABCDE0123456789FGHJK"
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"OG-ABCDE-01234-56789-FGHJK", norm, true},
		{"og-abcde-01234-56789-fghjk", norm, true},
		{"  OG ABCDE 01234 56789 FGHJK  ", norm, true},
		{"OGABCDE0123456789FGHJK", norm, true},
		{"ABCDE-01234-56789-FGHJK", norm, true},                           // prefix optional
		{"OG-ABCDE-O1234-56789-FGHJK", norm, true},                        // O → 0
		{"OG-ABCDE-0I234-56789-FGHJK", "ABCDE01234" + "56789FGHJK", true}, // I → 1
		{"abcdeo1234567s9fghjk", "ABCDE01234567S9FGHJK", true},            // o → 0, lowercase
		{"0G-ABCDE-01234-56789-FGHJK", norm, true},                        // zero-G prefix typed
		{"0GCDE0123456789FGHJK", "0GCDE0123456789FGHJK", true},            // bare code starting 0G is kept
		{"OG-ABCDE-01234-56789-FGHJ", "", false},                          // too short
		{"OG-ABCDE-01234-56789-FGHJKX", "", false},                        // too long
		{"OG-ABCDE-01234-56789-FGHJU", "", false},                         // U not in alphabet
		{"OG-ABCDE-01234-56789-FGHJ!", "", false},
		{"OG-ABCDE-01234-56789-FGHJＫ", "", false}, // full-width
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := NormalizeCode(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("NormalizeCode(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestAmbiguityMapping(t *testing.T) {
	a, ok1 := NormalizeCode("OG-ILOIL-OOOOO-LLLLL-IIIII")
	b, ok2 := NormalizeCode("OG-11011-00000-11111-11111")
	if !ok1 || !ok2 || a != b {
		t.Fatalf("ambiguity mapping: %q %v / %q %v", a, ok1, b, ok2)
	}
	if !bytes.Equal(HashCode(a), HashCode(b)) {
		t.Fatal("hashes differ")
	}
	if len(HashCode(a)) != 32 {
		t.Fatal("expected SHA-256 digest")
	}
	if p := CodePrefix("ABCDE0123456789FGHJK"); p != "OG-ABCDE" {
		t.Fatalf("prefix %q", p)
	}
}
