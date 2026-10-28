// Package transcript turns Claude Code's JSONL transcripts into the entries of
// protocol/spec.md section 6.2, and follows a session's transcript as Claude
// Code writes it.
//
// Claude Code's record format is not a stable API and changes between
// versions. Everything that depends on it is in this file; records it doesn't
// recognise are skipped.
package transcript

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

// MaxText is the most bytes of text a block carries, and the most bytes of
// any string inside a tool_use input.
const MaxText = 16 << 10

type record struct {
	Type                      string          `json:"type"`
	Subtype                   string          `json:"subtype"`
	UUID                      string          `json:"uuid"`
	Timestamp                 string          `json:"timestamp"`
	IsMeta                    bool            `json:"isMeta"`
	IsSidechain               bool            `json:"isSidechain"`
	IsCompactSummary          bool            `json:"isCompactSummary"`
	IsVisibleInTranscriptOnly bool            `json:"isVisibleInTranscriptOnly"`
	Message                   *message        `json:"message"`
	Content                   json.RawMessage `json:"content"` // system records
	DurationMS                *int64          `json:"durationMs"`
	Error                     *apiError       `json:"error"`
	RetryInMS                 float64         `json:"retryInMs"`
	RetryAttempt              int             `json:"retryAttempt"`
	MaxRetries                int             `json:"maxRetries"`
}

type message struct {
	Content json.RawMessage `json:"content"` // a string, or a list of blocks
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"` // tool_result: a string or a list of blocks
}

type apiError struct {
	Formatted string `json:"formatted"`
	Message   string `json:"message"`
}

var (
	commandName = regexp.MustCompile(`<command-name>([^<]*)</command-name>`)
	commandArgs = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
	commandOut  = regexp.MustCompile(`(?s)<local-command-stdout>(.*?)</local-command-stdout>`)
	leadingTag  = regexp.MustCompile(`^<[a-z][a-z0-9-]*>`)
)

// Normalize converts one transcript line into zero or one entries, without
// a sequence number.
func Normalize(line []byte) []proto.TranscriptEntry {
	var r record
	if json.Unmarshal(line, &r) != nil || r.IsSidechain {
		return nil
	}
	e := proto.TranscriptEntry{UUID: r.UUID, TS: r.Timestamp}
	switch r.Type {
	case "user":
		if r.IsMeta || r.IsCompactSummary || r.IsVisibleInTranscriptOnly || r.Message == nil {
			return nil
		}
		return userEntry(e, r.Message.Content)
	case "assistant":
		if r.Message == nil {
			return nil
		}
		e.Kind = proto.EntryAssistant
		e.Blocks = assistantBlocks(r.Message.Content)
	case "system":
		return systemEntry(e, r)
	default:
		return nil
	}
	if len(e.Blocks) == 0 {
		return nil
	}
	return []proto.TranscriptEntry{e}
}

func userEntry(e proto.TranscriptEntry, content json.RawMessage) []proto.TranscriptEntry {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return promptFromString(e, s)
	}
	var blocks []block
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	// A user record holds either what was typed or tool results.
	var prompt, results []proto.TranscriptBlock
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" && !isHarnessText(t) {
				prompt = append(prompt, textBlock("text", b.Text))
			}
		case "image":
			prompt = append(prompt, proto.TranscriptBlock{Type: "image"})
		case "tool_result":
			results = append(results, toolResult(b))
		}
	}
	switch {
	case len(results) > 0:
		e.Kind, e.Blocks = proto.EntryToolResult, results
	case len(prompt) > 0:
		e.Kind, e.Blocks = proto.EntryPrompt, prompt
	default:
		return nil
	}
	return []proto.TranscriptEntry{e}
}

// promptFromString handles a prompt stored as a plain string: typed text, a
// slash command, a local command's output, or text Claude Code's harness
// injected (task notifications, reminders), which is left out.
func promptFromString(e proto.TranscriptEntry, s string) []proto.TranscriptEntry {
	t := strings.TrimSpace(s)
	switch {
	case t == "":
		return nil
	case commandName.MatchString(t):
		cmd := strings.TrimSpace(commandName.FindStringSubmatch(t)[1])
		if m := commandArgs.FindStringSubmatch(t); m != nil && strings.TrimSpace(m[1]) != "" {
			cmd += " " + strings.TrimSpace(m[1])
		}
		e.Kind, e.Blocks = proto.EntryPrompt, []proto.TranscriptBlock{textBlock("text", cmd)}
	case commandOut.MatchString(t):
		out := strings.TrimSpace(commandOut.FindStringSubmatch(t)[1])
		if out == "" {
			return nil
		}
		e.Kind, e.Level, e.Blocks = proto.EntryNotice, "info", []proto.TranscriptBlock{textBlock("text", out)}
	case isHarnessText(t):
		return nil
	default:
		e.Kind, e.Blocks = proto.EntryPrompt, []proto.TranscriptBlock{textBlock("text", s)}
	}
	return []proto.TranscriptEntry{e}
}

// isHarnessText reports text that starts with a tag, which is how Claude
// Code marks content it adds itself rather than what the user typed.
func isHarnessText(t string) bool {
	return leadingTag.MatchString(t)
}

func assistantBlocks(content json.RawMessage) []proto.TranscriptBlock {
	var blocks []block
	if json.Unmarshal(content, &blocks) != nil {
		var s string
		if json.Unmarshal(content, &s) == nil && strings.TrimSpace(s) != "" {
			return []proto.TranscriptBlock{textBlock("text", s)}
		}
		return nil
	}
	var out []proto.TranscriptBlock
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, textBlock("text", b.Text))
			}
		case "thinking":
			// Without a summary only the signature is stored.
			if strings.TrimSpace(b.Thinking) != "" {
				out = append(out, textBlock("thinking", b.Thinking))
			}
		case "tool_use":
			input, cut := clipJSON(b.Input)
			out = append(out, proto.TranscriptBlock{Type: "tool_use", ID: b.ID, Name: b.Name, Input: input, Truncated: cut})
		}
	}
	return out
}

func toolResult(b block) proto.TranscriptBlock {
	var text string
	images := 0
	if json.Unmarshal(b.Content, &text) != nil {
		var parts []block
		json.Unmarshal(b.Content, &parts)
		var texts []string
		for _, p := range parts {
			switch p.Type {
			case "text":
				texts = append(texts, p.Text)
			case "image":
				images++
			}
		}
		text = strings.Join(texts, "\n")
	}
	isErr := b.IsError
	out := textBlock("tool_result", text)
	out.ToolUseID, out.IsError, out.Images = b.ToolUseID, &isErr, images
	return out
}

func systemEntry(e proto.TranscriptEntry, r record) []proto.TranscriptEntry {
	var content string
	json.Unmarshal(r.Content, &content)
	switch r.Subtype {
	case "compact_boundary":
		e.Kind, e.Reason = proto.EntryDivider, "compact"
	case "turn_duration":
		if r.DurationMS == nil {
			return nil
		}
		e.Kind, e.DurationMS = proto.EntryTurnEnd, r.DurationMS
	case "api_error":
		msg := "API error"
		if r.Error != nil {
			if r.Error.Formatted != "" {
				msg += " (" + r.Error.Formatted + ")"
			} else if r.Error.Message != "" {
				msg += ": " + r.Error.Message
			}
		}
		if r.MaxRetries > 0 {
			msg += fmt.Sprintf("; retrying in %.0f s (attempt %d of %d)", r.RetryInMS/1000, r.RetryAttempt, r.MaxRetries)
		}
		e.Kind, e.Level, e.Blocks = proto.EntryNotice, "error", []proto.TranscriptBlock{textBlock("text", msg)}
	case "informational", "local_command":
		if r.IsMeta || strings.TrimSpace(content) == "" {
			return nil
		}
		e.Kind, e.Level, e.Blocks = proto.EntryNotice, "info", []proto.TranscriptBlock{textBlock("text", content)}
	default:
		return nil
	}
	return []proto.TranscriptEntry{e}
}

func textBlock(typ, text string) proto.TranscriptBlock {
	t, cut := clip(text)
	return proto.TranscriptBlock{Type: typ, Text: &t, Truncated: cut}
}

// clip cuts s to MaxText bytes at a character boundary.
func clip(s string) (string, bool) {
	if len(s) <= MaxText {
		return s, false
	}
	n := MaxText
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// clipJSON cuts every string inside a tool input to MaxText bytes.
func clipJSON(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return json.RawMessage("{}"), false
	}
	if len(raw) <= MaxText {
		return raw, false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return json.RawMessage("{}"), true
	}
	cut := false
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			s, c := clip(x)
			cut = cut || c
			return s
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case map[string]any:
			for k := range x {
				x[k] = walk(x[k])
			}
		}
		return v
	}
	v = walk(v)
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}"), true
	}
	return b, cut
}
