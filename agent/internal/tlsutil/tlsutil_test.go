package tlsutil

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

// TestPinningFixtures checks Fingerprint against protocol/fixtures/pinning.json,
// which the app's tests read too.
func TestPinningFixtures(t *testing.T) {
	b, err := os.ReadFile("../../../protocol/fixtures/pinning.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name    string `json:"name"`
		CertDER string `json:"cert_der_base64"`
		SPKIDER string `json:"spki_der_base64"`
		FP      string `json:"fp"`
	}
	if err := json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			der, _ := base64.StdEncoding.DecodeString(f.CertDER)
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			if got := base64.StdEncoding.EncodeToString(cert.RawSubjectPublicKeyInfo); got != f.SPKIDER {
				t.Errorf("SPKI %s, want %s", got, f.SPKIDER)
			}
			if got := Fingerprint(cert); got != f.FP {
				t.Errorf("fingerprint %s, want %s", got, f.FP)
			}
		})
	}
}
