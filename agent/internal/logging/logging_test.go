package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

const tok = "hdt_Zm9vYmFyYmF6cXV4cXV1eGZvb2JhcmJhenF1eHF1dXg"

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"Bearer " + tok:                             "Bearer hdt_[redacted]",
		"push token hrp_abcdefghijklmnop ok":        "push token hrp_[redacted] ok",
		"hht_short":                                 "hht_short", // too short to be a credential
		"no secrets here":                           "no secrets here",
		"xhdt_abcdefghijklmnop":                     "xhdt_abcdefghijklmnop", // not at a word boundary
		"a hlt_aaaaaaaaaaaa and hrd_bbbbbbbbbbbbbb": "a hlt_[redacted] and hrd_[redacted]",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

type stringer struct{}

func (stringer) String() string { return "url?token=" + tok }

func TestLoggerRedactsEverything(t *testing.T) {
	for _, f := range []Format{Text, JSON} {
		var buf bytes.Buffer
		log := New(&buf, f, slog.LevelDebug)
		log.Info("got "+tok,
			"token", tok,
			"err", errors.New("radix said no to "+tok),
			"stringer", stringer{},
			slog.Group("req", "auth", "Bearer "+tok),
			"count", 3,
		)
		out := buf.String()
		if strings.Contains(out, tok) || strings.Count(out, "hdt_[redacted]") != 5 {
			t.Errorf("%s output leaks or misses a secret:\n%s", f, out)
		}
		if !strings.Contains(out, "3") {
			t.Errorf("%s lost an ordinary value:\n%s", f, out)
		}
	}
}

func TestParseFormat(t *testing.T) {
	if _, err := ParseFormat("xml"); err == nil {
		t.Fatal("xml accepted")
	}
	if f, _ := ParseFormat("json"); f != JSON {
		t.Fatal(f)
	}
}
