// Package auth implements federated login (generic OIDC + GitHub OAuth2 adapter),
// cookie sessions and the request authentication middleware. OmniGate never
// stores passwords (ADR-0005).
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"omnigate/internal/config"
	"omnigate/internal/identity"
)

// Provider is one configured login method.
type Provider interface {
	ID() string
	Type() string // "github" | "oidc"
	DisplayName() string
	// AuthCodeURL builds the IdP redirect. verifier is the PKCE code verifier.
	AuthCodeURL(ctx context.Context, state, nonce, verifier, redirectURI string) (string, error)
	// Exchange redeems the authorization code and returns the verified identity.
	Exchange(ctx context.Context, code, verifier, nonce, redirectURI string) (identity.External, error)
}

// ErrProvider wraps failures talking to an identity provider.
var ErrProvider = errors.New("auth: identity provider error")

// ---- GitHub (OAuth2; GitHub does not offer OIDC for user sign-in) ----

type githubProvider struct {
	cfg    config.GitHubProvider
	client *http.Client
}

func NewGitHub(cfg config.GitHubProvider, client *http.Client) Provider {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &githubProvider{cfg: cfg, client: client}
}

func (g *githubProvider) ID() string          { return "github" }
func (g *githubProvider) Type() string        { return "github" }
func (g *githubProvider) DisplayName() string { return "GitHub" }

func (g *githubProvider) oauth(redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     g.cfg.ClientID,
		ClientSecret: g.cfg.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: g.cfg.AuthURL, TokenURL: g.cfg.TokenURL, AuthStyle: oauth2.AuthStyleInParams},
		RedirectURL:  redirectURI,
		Scopes:       []string{"read:user", "user:email"},
	}
}

func (g *githubProvider) AuthCodeURL(_ context.Context, state, _, verifier, redirectURI string) (string, error) {
	return g.oauth(redirectURI).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), nil
}

func (g *githubProvider) Exchange(ctx context.Context, code, verifier, _, redirectURI string) (identity.External, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.client)
	tok, err := g.oauth(redirectURI).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return identity.External{}, fmt.Errorf("%w: github token exchange: %v", ErrProvider, err)
	}
	var u struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := g.getJSON(ctx, tok.AccessToken, "/user", &u); err != nil {
		return identity.External{}, err
	}
	if u.ID == 0 {
		return identity.External{}, fmt.Errorf("%w: github /user returned no id", ErrProvider)
	}
	ext := identity.External{
		Provider:  "github",
		Subject:   strconv.FormatInt(u.ID, 10),
		Login:     u.Login,
		Name:      u.Name,
		AvatarURL: u.AvatarURL,
	}
	// Only trust a verified primary email from /user/emails.
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := g.getJSON(ctx, tok.AccessToken, "/user/emails", &emails); err == nil {
		for _, e := range emails {
			if e.Primary && e.Verified {
				ext.Email, ext.EmailVerified = strings.ToLower(e.Email), true
			}
		}
	}
	return ext, nil
}

func (g *githubProvider) getJSON(ctx context.Context, token, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.cfg.APIURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: github %s: %v", ErrProvider, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: github %s: status %d", ErrProvider, path, resp.StatusCode)
	}
	return json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(v)
}

// ---- Generic OIDC ----

type oidcProvider struct {
	cfg    config.OIDCProvider
	client *http.Client

	mu       sync.Mutex
	provider *oidc.Provider // discovered lazily so a down IdP doesn't block startup
}

func NewOIDC(cfg config.OIDCProvider, client *http.Client) Provider {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &oidcProvider{cfg: cfg, client: client}
}

func (o *oidcProvider) ID() string          { return o.cfg.ID }
func (o *oidcProvider) Type() string        { return "oidc" }
func (o *oidcProvider) DisplayName() string { return o.cfg.DisplayName }

func (o *oidcProvider) discover(ctx context.Context) (*oidc.Provider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider != nil {
		return o.provider, nil
	}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, o.client), o.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: oidc discovery for %s: %v", ErrProvider, o.cfg.ID, err)
	}
	o.provider = p
	return p, nil
}

func (o *oidcProvider) oauth(p *oidc.Provider, redirectURI string) *oauth2.Config {
	scopes := o.cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	return &oauth2.Config{
		ClientID: o.cfg.ClientID, ClientSecret: o.cfg.ClientSecret,
		Endpoint: p.Endpoint(), RedirectURL: redirectURI, Scopes: scopes,
	}
}

func (o *oidcProvider) AuthCodeURL(ctx context.Context, state, nonce, verifier, redirectURI string) (string, error) {
	p, err := o.discover(ctx)
	if err != nil {
		return "", err
	}
	return o.oauth(p, redirectURI).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

func (o *oidcProvider) Exchange(ctx context.Context, code, verifier, nonce, redirectURI string) (identity.External, error) {
	p, err := o.discover(ctx)
	if err != nil {
		return identity.External{}, err
	}
	ctx = oidc.ClientContext(ctx, o.client)
	tok, err := o.oauth(p, redirectURI).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return identity.External{}, fmt.Errorf("%w: oidc token exchange: %v", ErrProvider, err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return identity.External{}, fmt.Errorf("%w: no id_token in token response", ErrProvider)
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: o.cfg.ClientID}).Verify(ctx, raw)
	if err != nil {
		return identity.External{}, fmt.Errorf("%w: id_token verification: %v", ErrProvider, err)
	}
	if idt.Nonce != nonce {
		return identity.External{}, fmt.Errorf("%w: nonce mismatch", ErrProvider)
	}
	var c struct {
		Email             string `json:"email"`
		EmailVerified     *bool  `json:"email_verified"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Picture           string `json:"picture"`
	}
	if err := idt.Claims(&c); err != nil {
		return identity.External{}, fmt.Errorf("%w: id_token claims: %v", ErrProvider, err)
	}
	return identity.External{
		Provider:      o.cfg.ID,
		Subject:       idt.Subject,
		Login:         c.PreferredUsername,
		Name:          c.Name,
		Email:         strings.ToLower(c.Email),
		EmailVerified: c.EmailVerified != nil && *c.EmailVerified,
		AvatarURL:     c.Picture,
	}, nil
}

// BuildProviders constructs providers from configuration, GitHub first.
func BuildProviders(a config.AuthConfig, client *http.Client) []Provider {
	var out []Provider
	if a.GitHub != nil {
		out = append(out, NewGitHub(*a.GitHub, client))
	}
	for _, o := range a.OIDC {
		out = append(out, NewOIDC(o, client))
	}
	return out
}
