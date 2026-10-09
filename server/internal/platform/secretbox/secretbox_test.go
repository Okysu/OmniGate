package secretbox

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func key(id string, b byte) Key { return Key{ID: id, Raw: bytes.Repeat([]byte{b}, 32)} }

func TestSealOpenRoundTrip(t *testing.T) {
	kr, err := New("test", []Key{key("k1", 1)})
	if err != nil {
		t.Fatal(err)
	}
	ct, err := kr.Seal([]byte("sk-secret"), "channel:1:api_key")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ct, "sk-secret") || !strings.HasPrefix(ct, "v1:k1:") {
		t.Fatalf("unexpected ciphertext %q", ct)
	}
	pt, err := kr.Open(ct, "channel:1:api_key")
	if err != nil || string(pt) != "sk-secret" {
		t.Fatalf("Open = %q, %v", pt, err)
	}
}

func TestAssociatedDataIsBound(t *testing.T) {
	kr, _ := New("test", []Key{key("k1", 1)})
	ct, _ := kr.Seal([]byte("x"), "channel:1:api_key")
	if _, err := kr.Open(ct, "channel:2:api_key"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("expected ErrDecrypt, got %v", err)
	}
}

func TestPurposeSeparation(t *testing.T) {
	a, _ := New("a", []Key{key("k1", 1)})
	b, _ := New("b", []Key{key("k1", 1)})
	ct, _ := a.Seal([]byte("x"), "")
	if _, err := b.Open(ct, ""); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("keyrings with different purposes must not share keys, got %v", err)
	}
}

func TestRotation(t *testing.T) {
	old, _ := New("p", []Key{key("k1", 1)})
	ct, _ := old.Seal([]byte("x"), "ad")

	rotated, _ := New("p", []Key{key("k2", 2), key("k1", 1)})
	if !rotated.NeedsRotation(ct) {
		t.Fatal("expected NeedsRotation for old key")
	}
	pt, err := rotated.Open(ct, "ad")
	if err != nil || string(pt) != "x" {
		t.Fatalf("old ciphertext unreadable after rotation: %v", err)
	}
	removed, _ := New("p", []Key{key("k2", 2)})
	if _, err := removed.Open(ct, "ad"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("expected ErrUnknownKey, got %v", err)
	}
}

func TestMalformed(t *testing.T) {
	kr, _ := New("p", []Key{key("k1", 1)})
	for _, in := range []string{"", "v1:k1", "v2:k1:abc", "v1:k1:!!!", "v1:k1:AAAA"} {
		if _, err := kr.Open(in, ""); err == nil {
			t.Errorf("Open(%q) succeeded", in)
		}
	}
}
