// Package logging builds the agent's logger. Every value it writes passes
// through Redact, so a credential that slips into a log line (inside an
// error message, a URL, a request body) is masked rather than written out.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"regexp"
)

// secretPattern matches every credential format hedera issues or handles:
// pairing tokens (hdt_), the local admin token (hlt_), hook tokens (hht_),
// and Radix push tokens and device secrets (hrp_, hrd_). Pairing codes are
// not prefixed, but are one-time and expire in minutes.
var secretPattern = regexp.MustCompile(`\b(hdt|hlt|hht|hrp|hrd)_[A-Za-z0-9_-]{8,}`)

// Redact masks credentials in s, keeping their prefix so a log still says
// which kind of secret was involved.
func Redact(s string) string {
	return secretPattern.ReplaceAllString(s, "${1}_[redacted]")
}

// Format is the log line format.
type Format string

const (
	Text Format = "text"
	JSON Format = "json"
)

// ParseFormat accepts "text" or "json".
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case Text, JSON:
		return Format(s), nil
	}
	return "", fmt.Errorf("log format must be text or json, not %q", s)
}

// New returns a logger writing to w that redacts every attribute and the
// message.
func New(w io.Writer, format Format, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr}
	if format == JSON {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

func redactAttr(_ []string, a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		if s := v.String(); secretPattern.MatchString(s) {
			a.Value = slog.StringValue(Redact(s))
		}
	case slog.KindAny:
		var s string
		switch x := v.Any().(type) {
		case error:
			s = x.Error()
		case fmt.Stringer:
			s = x.String()
		default:
			s = fmt.Sprint(x)
		}
		if secretPattern.MatchString(s) {
			a.Value = slog.StringValue(Redact(s))
		}
	}
	return a
}
