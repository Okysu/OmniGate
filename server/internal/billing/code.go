package billing

import (
	"crypto/rand"
	"crypto/sha256"
	"strings"
)

// Redeem codes look like OG-XXXXX-XXXXX-XXXXX-XXXXX: 20 Crockford Base32 symbols
// (100 bits from crypto/rand). Only the SHA-256 of the normalized 20-symbol string
// is stored (ADR-0008); the plaintext is shown once, at batch creation.
const (
	crockford   = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	codeSymbols = 20
	codeGroup   = 5
	codePrefix  = "OG"
)

// GenerateCode returns a new display-form code and its normalized form.
func GenerateCode() (display, normalized string, err error) {
	var b [codeSymbols]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	// 256 is a multiple of 32, so the low 5 bits of a uniform byte are uniform.
	sym := make([]byte, codeSymbols)
	for i, v := range b {
		sym[i] = crockford[v&31]
	}
	normalized = string(sym)
	return formatCode(normalized), normalized, nil
}

func formatCode(normalized string) string {
	var sb strings.Builder
	sb.WriteString(codePrefix)
	for i := 0; i < len(normalized); i += codeGroup {
		sb.WriteByte('-')
		sb.WriteString(normalized[i:min(i+codeGroup, len(normalized))])
	}
	return sb.String()
}

// NormalizeCode canonicalizes user input: case-insensitive, spaces and hyphens
// ignored, optional leading "OG", and Crockford's ambiguity mapping (I/L → 1,
// O → 0). It reports false when the input cannot be a valid code.
func NormalizeCode(in string) (string, bool) {
	var sb strings.Builder
	for _, r := range strings.ToUpper(in) {
		switch r {
		case ' ', '-', '\t', '\n', '\r', ' ', '　':
			continue
		}
		if r > 0x7f {
			return "", false
		}
		sb.WriteRune(r)
	}
	s := sb.String()
	// Strip the "OG" prefix only when a prefix is actually present (22 symbols),
	// so a bare code that happens to start with "0G"/"OG" is not truncated.
	if len(s) == codeSymbols+len(codePrefix) && (strings.HasPrefix(s, "OG") || strings.HasPrefix(s, "0G")) {
		s = s[len(codePrefix):]
	}
	if len(s) != codeSymbols {
		return "", false
	}
	out := []byte(s)
	for i, c := range out {
		switch c {
		case 'I', 'L':
			c = '1'
		case 'O':
			c = '0'
		}
		if strings.IndexByte(crockford, c) < 0 {
			return "", false
		}
		out[i] = c
	}
	return string(out), true
}

// HashCode returns the stored digest of a normalized code.
func HashCode(normalized string) []byte {
	h := sha256.Sum256([]byte(normalized))
	return h[:]
}

// CodePrefix returns the display prefix ("OG-ABCDE") of a normalized code.
func CodePrefix(normalized string) string {
	return codePrefix + "-" + normalized[:codeGroup]
}
