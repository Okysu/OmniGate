package control

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"omnigate/internal/platform/secretbox"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestJevClientFailurePaths(t *testing.T) {
	box, err := secretbox.New("test", []secretbox.Key{{ID: "test", Raw: make([]byte, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := box.Seal([]byte("jev-key-value"), "jev-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		status       int
		body, reason string
	}{
		{"unauthorized", 401, "secret upstream reply", "http_401"},
		{"forbidden", 403, "secret upstream reply", "http_403"},
		{"server error", 500, "secret upstream reply", "http_500"},
		{"invalid json", 200, "not json", "bad_json"},
		{"missing answer", 200, `{"answers":{}}`, "missing_c0"},
		{"missing probability", 200, `{"answers":{"c0":{}}}`, "missing_c0"},
		{"large response", 200, strings.Repeat("x", (1<<20)+1), "invalid_response"},
		{"network", 0, "", "fetch_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(nil, box, nil, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != OfficialBaseURL+"/systemone" || r.Header.Get("Authorization") != "Bearer jev-key-value" {
					t.Fatal("wire contract changed")
				}
				if _, ok := r.Context().Deadline(); !ok {
					t.Fatal("missing timeout")
				}
				if tc.status == 0 {
					return nil, errors.New("secret network detail")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			}))
			p := snapshot{Settings: Settings{Config: DefaultConfig()}, Secret: secret}
			p.Categories = []string{"check"}
			_, _, _, err := s.classify(context.Background(), p, "text")
			if err == nil || err.Error() != tc.reason {
				t.Fatalf("reason=%v", err)
			}
		})
	}
	// Missing or unreadable keys are classifier failures, never user evidence.
	s := New(nil, box, nil, roundTripFunc(func(r *http.Request) (*http.Response, error) { t.Fatal("request with invalid key"); return nil, nil }))
	if _, _, _, err := s.classify(context.Background(), snapshot{Settings: Settings{Config: DefaultConfig()}}, "text"); err == nil || err.Error() != "key_unavailable" {
		t.Fatalf("key failure=%v", err)
	}
}
