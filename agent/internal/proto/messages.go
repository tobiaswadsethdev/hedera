package proto

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Request types.
const (
	THello             = "hello"
	TSessionList       = "session.list"
	TSessionCreate     = "session.create"
	TSessionAttach     = "session.attach"
	TSessionDetach     = "session.detach"
	TSessionInput      = "session.input"
	TSessionResize     = "session.resize"
	TSessionClose      = "session.close"
	TSessionRemove     = "session.remove"
	TEventsSubscribe   = "events.subscribe"
	TEventsUnsubscribe = "events.unsubscribe"
	TPushConfigure     = "push.configure"
	TPushStatus        = "push.status"
	TPushRemove        = "push.remove"
	TPairingUnpair     = "pairing.unpair"
	TSessionResumable  = "session.resumable"
	TSessionResume     = "session.resume"
	TSessionForget     = "session.forget"

	TTranscriptSubscribe   = "transcript.subscribe"
	TTranscriptUnsubscribe = "transcript.unsubscribe"
	TTranscriptRead        = "transcript.read"
)

// Agent-to-client message types.
const (
	TResponse     = "res"
	TEvent        = "event"
	TLagged       = "lagged"
	TDetached     = "detached"
	TEventsLagged = "events.lagged"
	TTranscript   = "transcript"
)

// Event types.
const (
	EvSessionCreated = "session.created"
	EvSessionUpdated = "session.updated"
	EvSessionResized = "session.resized"
	EvSessionExited  = "session.exited"
	EvSessionRemoved = "session.removed"

	EvClaudeTurnFinished = "claude.turn_finished"
	EvClaudeNeedsInput   = "claude.needs_input"
	EvClaudeFailed       = "claude.failed"
	EvClaudeEnded        = "claude.ended"
)

// Session kinds and statuses.
const (
	KindShell  = "shell"
	KindClaude = "claude"

	StatusRunning    = "running"
	StatusStarting   = "starting"
	StatusWorking    = "working"
	StatusIdle       = "idle"
	StatusNeedsInput = "needs_input"
	StatusFailed     = "failed"
	StatusExited     = "exited"

	// ClaudeEnded is a Claude state status only: Claude Code has exited but
	// the session's process (e.g. the shell it ran in) may still be alive.
	ClaudeEnded = "ended"
)

// Close codes.
const (
	CloseHandshake         = 4001
	CloseRevoked           = 4003
	CloseProtocolViolation = 4008
	CloseWriteTimeout      = 4009
)

// Header holds the fields common to every control message. Requests are
// decoded twice: once into Header, then into the typed params.
type Header struct {
	T  string  `json:"t"`
	ID *uint64 `json:"id,omitempty"`
}

type VersionRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type ClientInfo struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Platform string `json:"platform,omitempty"`
}

type Hello struct {
	Protocol VersionRange `json:"protocol"`
	Client   ClientInfo   `json:"client"`
}

type AgentInfo struct {
	Version  string `json:"version"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Hostname string `json:"hostname"`
}

type Limits struct {
	MaxSessions     int `json:"max_sessions"`
	ScrollbackBytes int `json:"scrollback_bytes"`
	MaxFrame        int `json:"max_frame"`
}

type HelloResult struct {
	Protocol     int       `json:"protocol"`
	MachineID    string    `json:"machine_id"`
	BootID       string    `json:"boot_id"`
	Agent        AgentInfo `json:"agent"`
	Capabilities []string  `json:"capabilities"`
	Limits       Limits    `json:"limits"`
	ServerTime   string    `json:"server_time"`
}

type Size struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

type Exit struct {
	Code        *int    `json:"code"`
	Signal      *string `json:"signal"`
	At          string  `json:"at"`
	InitiatedBy string  `json:"initiated_by"`
}

type NeedsInput struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type ClaudeError struct {
	Error        string  `json:"error"`
	ErrorDetails *string `json:"error_details"`
}

type ClaudeState struct {
	Status             string       `json:"status"`
	SessionID          *string      `json:"session_id"`
	Turn               int          `json:"turn"`
	NeedsInput         *NeedsInput  `json:"needs_input"`
	LastMessagePreview *string      `json:"last_message_preview"`
	LastError          *ClaudeError `json:"last_error"`
	EndReason          *string      `json:"end_reason"`
	Transcript         bool         `json:"transcript,omitempty"`
}

type Session struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	Title        string       `json:"title"`
	Cwd          string       `json:"cwd"`
	Argv         []string     `json:"argv"`
	Pid          int          `json:"pid"`
	CreatedAt    string       `json:"created_at"`
	Size         Size         `json:"size"`
	Status       string       `json:"status"`
	OutputOffset uint64       `json:"output_offset"`
	Attached     int          `json:"attached"`
	Claude       *ClaudeState `json:"claude"`
	Exit         *Exit        `json:"exit"`
}

type ListResult struct {
	Sessions []Session `json:"sessions"`
}

type CreateRequest struct {
	Kind  string            `json:"kind"`
	Cwd   string            `json:"cwd,omitempty"`
	Argv  []string          `json:"argv,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
	Cols  int               `json:"cols,omitempty"`
	Rows  int               `json:"rows,omitempty"`
	Title string            `json:"title,omitempty"`
}

type CreateResult struct {
	Session Session `json:"session"`
}

type AttachRequest struct {
	SessionID  string  `json:"session_id"`
	FromOffset *uint64 `json:"from_offset,omitempty"`
	Cols       int     `json:"cols,omitempty"`
	Rows       int     `json:"rows,omitempty"`
}

type Replay struct {
	From      uint64 `json:"from"`
	To        uint64 `json:"to"`
	Truncated bool   `json:"truncated"`
}

type AttachResult struct {
	Channel uint32  `json:"channel"`
	Session Session `json:"session"`
	Replay  Replay  `json:"replay"`
}

type DetachRequest struct {
	Channel uint32 `json:"channel"`
}

type InputRequest struct {
	SessionID string `json:"session_id"`
	Data      string `json:"data"`
	Submit    bool   `json:"submit,omitempty"`
	IfTurn    *int   `json:"if_turn,omitempty"`
}

type ResizeRequest struct {
	SessionID string `json:"session_id"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type CloseRequest struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode,omitempty"`
}

type RemoveRequest struct {
	SessionID string `json:"session_id"`
}

type SubscribeRequest struct {
	SinceSeq *uint64 `json:"since_seq,omitempty"`
}

type SubscribeResult struct {
	Seq   uint64 `json:"seq"`
	Reset bool   `json:"reset"`
}

// Resumable is a Claude session that was live when the agent stopped.
type Resumable struct {
	ClaudeSessionID string `json:"claude_session_id"`
	Cwd             string `json:"cwd"`
	Title           string `json:"title"`
	LastSeenAt      string `json:"last_seen_at"`
}

type ResumableResult struct {
	Resumable []Resumable `json:"resumable"`
}

type ResumeRequest struct {
	ClaudeSessionID string `json:"claude_session_id"`
	Cols            int    `json:"cols,omitempty"`
	Rows            int    `json:"rows,omitempty"`
}

type ForgetRequest struct {
	ClaudeSessionID string `json:"claude_session_id"`
}

// Push messages.

type PushConfigureRequest struct {
	RadixURL  string          `json:"radix_url"`
	PushToken string          `json:"push_token"`
	PushKey   string          `json:"push_key,omitempty"`
	Prefs     map[string]bool `json:"prefs,omitempty"`
}

type PushStatus struct {
	Configured    bool            `json:"configured"`
	RadixURL      string          `json:"radix_url,omitempty"`
	Label         string          `json:"label,omitempty"`
	Prefs         map[string]bool `json:"prefs,omitempty"`
	Broken        string          `json:"broken,omitempty"`
	LastSuccessAt string          `json:"last_success_at,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
	Encrypted     bool            `json:"encrypted,omitempty"`
	Queued        int             `json:"queued"`
}

// Event is the envelope for every event type. Fields that a type does not use
// are omitted.
type Event struct {
	T         string   `json:"t"`
	Seq       uint64   `json:"seq"`
	TS        string   `json:"ts"`
	Type      string   `json:"type"`
	Session   *Session `json:"session,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Cols      int      `json:"cols,omitempty"`
	Rows      int      `json:"rows,omitempty"`

	// claude.* events
	Turn         *int    `json:"turn,omitempty"`
	Preview      *string `json:"preview,omitempty"`
	Kind         string  `json:"kind,omitempty"`
	Message      string  `json:"message,omitempty"`
	Error        string  `json:"error,omitempty"`
	ErrorDetails *string `json:"error_details,omitempty"`
}

type Lagged struct {
	T         string `json:"t"`
	Channel   uint32 `json:"channel"`
	SessionID string `json:"session_id"`
	Offset    uint64 `json:"offset"`
}

type Detached struct {
	T         string `json:"t"`
	Channel   uint32 `json:"channel"`
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
}

type EventsLagged struct {
	T   string `json:"t"`
	Seq uint64 `json:"seq"`
}

// Response is the decoded envelope of a "res" message. The result fields stay
// in the raw message; decode them with json.Unmarshal into the result type.
type Response struct {
	T     string  `json:"t"`
	Re    *uint64 `json:"re"`
	OK    bool    `json:"ok"`
	Error *Error  `json:"error,omitempty"`
}

// MarshalResult encodes a success response. result must marshal to a JSON
// object, or be nil.
func MarshalResult(re uint64, result any) ([]byte, error) {
	return appendObject(`{"t":"res","re":`+strconv.FormatUint(re, 10)+`,"ok":true`, result)
}

// MarshalRequest encodes a request. params must marshal to a JSON object, or
// be nil.
func MarshalRequest(t string, id uint64, params any) ([]byte, error) {
	tb, _ := json.Marshal(t)
	prefix := `{"t":` + string(tb) + `,"id":` + strconv.FormatUint(id, 10)
	return appendObject(prefix, params)
}

func appendObject(prefix string, v any) ([]byte, error) {
	if v == nil {
		return []byte(prefix + "}"), nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(body) < 2 || body[0] != '{' {
		return nil, fmt.Errorf("proto: %T is not a JSON object", v)
	}
	if len(body) == 2 {
		return []byte(prefix + "}"), nil
	}
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix...)
	out = append(out, ',')
	return append(out, body[1:]...), nil
}

// MarshalError encodes a failure response.
func MarshalError(re uint64, e *Error) []byte {
	b, _ := json.Marshal(struct {
		T     string `json:"t"`
		Re    uint64 `json:"re"`
		OK    bool   `json:"ok"`
		Error *Error `json:"error"`
	}{TResponse, re, false, e})
	return b
}

// FormatTime renders t the way the protocol expects: RFC 3339 in UTC with
// millisecond precision.
func FormatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// Transcript entry kinds (section 6.2).
const (
	EntryPrompt     = "prompt"
	EntryAssistant  = "assistant"
	EntryToolResult = "tool_result"
	EntryTurnEnd    = "turn_end"
	EntryDivider    = "divider"
	EntryNotice     = "notice"
)

// TranscriptBlock is one block of a transcript entry. Which fields are set
// depends on Type: text and thinking use Text; tool_use uses ID, Name and
// Input; tool_result uses ToolUseID, IsError, Text and Images.
type TranscriptBlock struct {
	Type      string          `json:"type"`
	Text      *string         `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   *bool           `json:"is_error,omitempty"`
	Images    int             `json:"images,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}

type TranscriptEntry struct {
	Seq        uint64            `json:"seq"`
	UUID       string            `json:"uuid,omitempty"`
	TS         string            `json:"ts,omitempty"`
	Kind       string            `json:"kind"`
	Blocks     []TranscriptBlock `json:"blocks,omitempty"`
	DurationMS *int64            `json:"duration_ms,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Level      string            `json:"level,omitempty"`
}

type TranscriptSubscribeRequest struct {
	SessionID string  `json:"session_id"`
	Since     *uint64 `json:"since,omitempty"`
	Tail      *int    `json:"tail,omitempty"`
}

type TranscriptSubscribeResult struct {
	Seq      uint64 `json:"seq"`
	FirstSeq uint64 `json:"first_seq"`
	Reset    bool   `json:"reset"`
}

type TranscriptUnsubscribeRequest struct {
	SessionID string `json:"session_id"`
}

type TranscriptReadRequest struct {
	SessionID string `json:"session_id"`
	Before    uint64 `json:"before"`
	Limit     *int   `json:"limit,omitempty"`
}

type TranscriptReadResult struct {
	Entries []TranscriptEntry `json:"entries"`
}

// Transcript carries new entries for a transcript subscription.
type Transcript struct {
	T         string            `json:"t"`
	SessionID string            `json:"session_id"`
	Entries   []TranscriptEntry `json:"entries"`
}
