// Package ids generates random identifiers and secrets.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"strings"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// New returns prefix followed by n random lowercase base32 characters
// (alphabet a–z, 2–7), e.g. New("s_", 10) → "s_4fj7q2m3xa".
func New(prefix string, n int) string {
	raw := make([]byte, (n*5+7)/8)
	rand.Read(raw)
	return prefix + strings.ToLower(b32.EncodeToString(raw))[:n]
}

// Secret returns prefix followed by 32 random bytes, base64url without padding.
func Secret(prefix string) string {
	raw := make([]byte, 32)
	rand.Read(raw)
	return prefix + base64.RawURLEncoding.EncodeToString(raw)
}

// Code returns a one-time code: 20 random bytes as lowercase base32.
func Code() string {
	raw := make([]byte, 20)
	rand.Read(raw)
	return strings.ToLower(b32.EncodeToString(raw))
}

func SessionID() string { return New("s_", 10) }
func MachineID() string { return New("m_", 16) }
func BootID() string    { return New("b_", 10) }
func PairingID() string { return New("p_", 8) }
