package auth

import (
	"testing"

	"omnigate/internal/config"
	"omnigate/internal/identity"
)

func TestSafeRedirect(t *testing.T) {
	cases := map[string]string{
		"":                       "/console",
		"/console/channels":      "/console/channels",
		"/console/users?page=2":  "/console/users?page=2",
		"//evil.example":         "/console",
		`/\evil.example`:         "/console",
		"https://evil.example/x": "/console",
		"javascript:alert(1)":    "/console",
		"/ok\r\nSet-Cookie: x":   "/console",
		"/api/auth/logout":       "/console",
		"relative":               "/console",
		"/":                      "/",
		"/./\\evil.com":          "/console",
		"/console/\\evil":        "/console",
		"/x/../api/auth/logout":  "/console",
		"/console/a/../b?x=1#h":  "/console/b?x=1#h",
		"/ /evil.com":            "/console",
		"/console?next=//evil":   "/console?next=//evil",
	}
	for in, want := range cases {
		if got := SafeRedirect(in); got != want {
			t.Errorf("SafeRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchAny(t *testing.T) {
	gh := identity.External{Provider: "github", Subject: "42", Login: "Octocat", Email: "o@x.io", EmailVerified: true}
	oidc := identity.External{Provider: "corp", Subject: "sub-1", Login: "octocat", Email: "o@x.io"}
	m := func(p, v string) []config.IdentityMatcher { return []config.IdentityMatcher{{Provider: p, Value: v}} }

	cases := []struct {
		name string
		ms   []config.IdentityMatcher
		ext  identity.External
		want bool
	}{
		{"github login case-insensitive", m("github", "octocat"), gh, true},
		{"github numeric id", m("github-id", "42"), gh, true},
		{"github id mismatch", m("github-id", "43"), gh, false},
		{"github login does not match oidc", m("github", "octocat"), oidc, false},
		{"oidc subject", m("corp", "sub-1"), oidc, true},
		{"oidc preferred_username is not trusted", m("corp", "octocat"), oidc, false},
		{"verified email", m("email", "O@X.io"), gh, true},
		{"unverified email ignored", m("email", "o@x.io"), oidc, false},
	}
	for _, c := range cases {
		if got := matchAny(c.ms, c.ext); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "app.localhost": true, "127.0.0.1": true, "::1": true, "[::1]": true,
		"gw.example.com": false, "10.0.0.1": false, "localhost.evil.com": false,
	} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v", host, got)
		}
	}
}
