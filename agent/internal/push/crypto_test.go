package push

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type cryptoFixture struct {
	Name      string `json:"name"`
	Key       string `json:"key"`
	Nonce     string `json:"nonce,omitempty"`
	MachineID string `json:"machine_id"`
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
	Turn      *int   `json:"turn,omitempty"`
	Title     string `json:"title,omitempty"`
	Subtitle  string `json:"subtitle,omitempty"`
	Body      string `json:"body,omitempty"`
	Plaintext string `json:"plaintext,omitempty"`
	Encrypted string `json:"encrypted"`
	// Fails marks a case whose additional data doesn't match: opening must fail.
	Fails bool `json:"fails,omitempty"`
}

// TestCryptoFixtures checks protocol/fixtures/push-crypto.json, which the
// app's tests decrypt too.
func TestCryptoFixtures(t *testing.T) {
	b, err := os.ReadFile("../../../protocol/fixtures/push-crypto.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []cryptoFixture
	if err := json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 3 {
		t.Fatalf("only %d fixtures", len(fixtures))
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			key, err := ParseKey(f.Key)
			if err != nil {
				t.Fatal(err)
			}
			n := Notification{Kind: f.Kind, SessionID: f.SessionID, Turn: f.Turn, Title: f.Title, Subtitle: f.Subtitle, Body: f.Body}
			if f.Fails {
				if _, _, _, err := Decrypt(key, f.Encrypted, f.MachineID, n); err == nil {
					t.Fatal("opened a ciphertext under the wrong additional data")
				}
				return
			}
			nonce, _ := base64.RawURLEncoding.DecodeString(f.Nonce)
			got, err := seal(key, nonce, f.MachineID, n)
			if err != nil {
				t.Fatal(err)
			}
			if got != f.Encrypted {
				t.Errorf("encrypted = %s, want %s", got, f.Encrypted)
			}
			title, subtitle, body, err := Decrypt(key, f.Encrypted, f.MachineID, n)
			if err != nil {
				t.Fatal(err)
			}
			if title != clip(f.Title, 100) || subtitle != clip(f.Subtitle, 100) || body != clip(f.Body, 500) {
				t.Errorf("decrypted %q %q %q", title, subtitle, body)
			}
			if p := string(marshalText(pushText{clip(f.Title, 100), clip(f.Subtitle, 100), clip(f.Body, 500)})); p != f.Plaintext {
				t.Errorf("plaintext = %s, want %s", p, f.Plaintext)
			}
		})
	}
}

func TestEncryptUsesAFreshNonce(t *testing.T) {
	key := make([]byte, 32)
	n := Notification{Kind: KindTest, Title: "t", Body: "b"}
	a, _ := Encrypt(key, "m_x", n)
	b, _ := Encrypt(key, "m_x", n)
	if a == b {
		t.Fatal("two encryptions were identical")
	}
}

func TestEncryptStaysWithinRadixLimit(t *testing.T) {
	key := make([]byte, 32)
	// 500 four-byte characters, each escaped by nothing: about 2 KB of text,
	// which with the title would exceed the limit after base64.
	n := Notification{Kind: KindClaudeNeedsInput, Title: strings.Repeat("𝄞", 200), Subtitle: strings.Repeat("𝄞", 200), Body: strings.Repeat("𝄞", 600)}
	enc, err := Encrypt(key, "m_x", n)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) > maxEncrypted {
		t.Fatalf("encrypted is %d characters", len(enc))
	}
	title, _, body, err := Decrypt(key, enc, "m_x", n)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(body, "…") || len([]rune(title)) != 100 {
		t.Fatalf("title %d runes, body %q…", len([]rune(title)), body[:8])
	}
}

func TestParseKey(t *testing.T) {
	if _, err := ParseKey(base64.RawURLEncoding.EncodeToString(make([]byte, 32))); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "short", base64.RawURLEncoding.EncodeToString(make([]byte, 16)), base64.StdEncoding.EncodeToString(make([]byte, 32))} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
