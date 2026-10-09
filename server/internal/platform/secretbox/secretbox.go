// Package secretbox provides authenticated encryption for secrets at rest
// (channel credentials, plugin secrets) and short-lived signed state (OAuth state).
//
// Ciphertext format: "v1:<kid>:<base64url(nonce || sealed)>". The key id lets us
// rotate master keys: new data is sealed with the active key, old data stays
// readable while its key remains configured. Every Seal binds an associated-data
// string (e.g. "channel_secret:<channel-id>:api_key") so a ciphertext cannot be
// replayed into a different field or row.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrMalformed  = errors.New("secretbox: malformed ciphertext")
	ErrUnknownKey = errors.New("secretbox: unknown key id")
	ErrDecrypt    = errors.New("secretbox: decryption failed")
)

type Key struct {
	ID  string
	Raw []byte // 32 bytes
}

// Keyring holds the master keys. Purpose-specific subkeys are derived with HKDF so
// that different subsystems never share raw key material.
type Keyring struct {
	purpose string
	active  string
	aeads   map[string]cipher.AEAD
}

// New builds a keyring for the given purpose. keys[0] is the active key.
func New(purpose string, keys []Key) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("secretbox: at least one key is required")
	}
	kr := &Keyring{purpose: purpose, active: keys[0].ID, aeads: map[string]cipher.AEAD{}}
	for _, k := range keys {
		if len(k.Raw) != 32 {
			return nil, fmt.Errorf("secretbox: key %q must be 32 bytes", k.ID)
		}
		if strings.Contains(k.ID, ":") {
			return nil, fmt.Errorf("secretbox: key id %q must not contain ':'", k.ID)
		}
		sub, err := hkdf.Key(sha256.New, k.Raw, nil, "omnigate/"+purpose, 32)
		if err != nil {
			return nil, err
		}
		block, err := aes.NewCipher(sub)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		kr.aeads[k.ID] = aead
	}
	return kr, nil
}

// Seal encrypts plaintext with the active key, binding it to ad.
func (k *Keyring) Seal(plaintext []byte, ad string) (string, error) {
	aead := k.aeads[k.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, plaintext, []byte(ad))
	return "v1:" + k.active + ":" + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal with the same ad.
func (k *Keyring) Open(ciphertext, ad string) ([]byte, error) {
	parts := strings.SplitN(ciphertext, ":", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return nil, ErrMalformed
	}
	aead, ok := k.aeads[parts[1]]
	if !ok {
		return nil, ErrUnknownKey
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrMalformed
	}
	pt, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(ad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// NeedsRotation reports whether ciphertext was sealed with a non-active key.
func (k *Keyring) NeedsRotation(ciphertext string) bool {
	parts := strings.SplitN(ciphertext, ":", 3)
	return len(parts) != 3 || parts[1] != k.active
}
