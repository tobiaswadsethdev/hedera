package push

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"unicode/utf8"
)

// End-to-end encrypted notification text (protocol/spec.md section 7.4).

const (
	keySize = 32
	// maxEncrypted is the longest `encrypted` Radix accepts, which keeps the
	// APNs payload under its 4 KB limit.
	maxEncrypted = 3000
)

// ParseKey decodes a push_key: 32 bytes, base64url without padding.
func ParseKey(s string) ([]byte, error) {
	k, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(k) != keySize {
		return nil, errors.New("push_key must be 32 bytes in base64url without padding")
	}
	return k, nil
}

// pushText is what gets encrypted.
type pushText struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Body     string `json:"body"`
}

// additionalData binds the ciphertext to its machine, kind, session and turn.
func additionalData(machineID string, n Notification) []byte {
	turn := ""
	if n.Turn != nil {
		turn = strconv.Itoa(*n.Turn)
	}
	return []byte("hedera-push-v1\n" + machineID + "\n" + n.Kind + "\n" + n.SessionID + "\n" + turn)
}

// clip cuts s to at most n code points, ending with "…" when cut, like
// Radix does for plaintext.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func marshalText(t pushText) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(t)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// seal encrypts n's text under key with the given nonce.
func seal(key, nonce []byte, machineID string, n Notification) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	limit := 500
	for {
		t := pushText{Title: clip(n.Title, 100), Subtitle: clip(n.Subtitle, 100), Body: clip(n.Body, limit)}
		out := gcm.Seal(append([]byte(nil), nonce...), nonce, marshalText(t), additionalData(machineID, n))
		enc := base64.RawURLEncoding.EncodeToString(out)
		if len(enc) <= maxEncrypted || limit <= 1 {
			return enc, nil
		}
		// Many multi-byte characters: shorten the body until it fits.
		limit /= 2
	}
}

// Encrypt seals n's text with a fresh random nonce.
func Encrypt(key []byte, machineID string, n Notification) (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return seal(key, nonce, machineID, n)
}

// Decrypt reverses Encrypt; the app does this on the phone, the agent only
// in tests.
func Decrypt(key []byte, enc, machineID string, n Notification) (title, subtitle, body string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(raw) < 12+16 {
		return "", "", "", errors.New("malformed ciphertext")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", "", err
	}
	plain, err := gcm.Open(nil, raw[:12], raw[12:], additionalData(machineID, n))
	if err != nil {
		return "", "", "", err
	}
	var t pushText
	if err := json.Unmarshal(plain, &t); err != nil {
		return "", "", "", err
	}
	return t.Title, t.Subtitle, t.Body, nil
}
