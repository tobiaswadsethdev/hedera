package proto

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const fixtureDir = "../../../protocol/fixtures"

type messageFixture struct {
	Name       string          `json:"name"`
	Direction  string          `json:"direction"`
	ResponseTo string          `json:"response_to"`
	Message    json.RawMessage `json:"message"`
}

type binaryFixture struct {
	Name       string `json:"name"`
	FrameHex   string `json:"frame_hex"`
	Op         string `json:"op"`
	Channel    uint32 `json:"channel"`
	Offset     uint64 `json:"offset"`
	PayloadHex string `json:"payload_hex"`
}

func readFixture(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

var requestTypes = map[string]func() any{
	THello:                 func() any { return &Hello{} },
	TSessionList:           func() any { return &struct{}{} },
	TSessionCreate:         func() any { return &CreateRequest{} },
	TSessionAttach:         func() any { return &AttachRequest{} },
	TSessionDetach:         func() any { return &DetachRequest{} },
	TSessionInput:          func() any { return &InputRequest{} },
	TSessionResize:         func() any { return &ResizeRequest{} },
	TSessionClose:          func() any { return &CloseRequest{} },
	TSessionRemove:         func() any { return &RemoveRequest{} },
	TEventsSubscribe:       func() any { return &SubscribeRequest{} },
	TEventsUnsubscribe:     func() any { return &struct{}{} },
	TPushConfigure:         func() any { return &PushConfigureRequest{} },
	TPushStatus:            func() any { return &struct{}{} },
	TPushRemove:            func() any { return &struct{}{} },
	TPairingUnpair:         func() any { return &struct{}{} },
	TTranscriptSubscribe:   func() any { return &TranscriptSubscribeRequest{} },
	TTranscriptUnsubscribe: func() any { return &TranscriptUnsubscribeRequest{} },
	TTranscriptRead:        func() any { return &TranscriptReadRequest{} },
	TSessionResumable:      func() any { return &struct{}{} },
	TSessionResume:         func() any { return &ResumeRequest{} },
	TSessionForget:         func() any { return &ForgetRequest{} },
}

var resultTypes = map[string]func() any{
	THello:               func() any { return &HelloResult{} },
	TSessionList:         func() any { return &ListResult{} },
	TSessionCreate:       func() any { return &CreateResult{} },
	TSessionAttach:       func() any { return &AttachResult{} },
	TSessionResize:       func() any { return nil },
	TEventsSubscribe:     func() any { return &SubscribeResult{} },
	TPushConfigure:       func() any { return &PushStatus{} },
	TPushStatus:          func() any { return &PushStatus{} },
	TSessionResumable:    func() any { return &ResumableResult{} },
	TTranscriptSubscribe: func() any { return &TranscriptSubscribeResult{} },
	TTranscriptRead:      func() any { return &TranscriptReadResult{} },
}

// TestMessageFixtures decodes every golden message into its Go type and
// re-encodes it, which proves the Go types carry every field in the contract.
func TestMessageFixtures(t *testing.T) {
	var fixtures []messageFixture
	readFixture(t, "messages.json", &fixtures)
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			var hdr struct {
				T  string  `json:"t"`
				ID *uint64 `json:"id"`
				Re *uint64 `json:"re"`
				OK bool    `json:"ok"`
			}
			if err := json.Unmarshal(f.Message, &hdr); err != nil {
				t.Fatal(err)
			}
			var got []byte
			switch {
			case f.Direction == "client_to_agent":
				mk, ok := requestTypes[hdr.T]
				if !ok {
					t.Fatalf("no Go type for request %q", hdr.T)
				}
				v := mk()
				mustUnmarshal(t, f.Message, v)
				got = mergeObject(t, map[string]any{"t": hdr.T, "id": *hdr.ID}, v)
			case hdr.T == TResponse && !hdr.OK:
				var r Response
				mustUnmarshal(t, f.Message, &r)
				got = MarshalError(*r.Re, r.Error)
			case hdr.T == TResponse:
				mk, ok := resultTypes[f.ResponseTo]
				if !ok {
					t.Fatalf("no Go type for result of %q", f.ResponseTo)
				}
				v := mk()
				if v != nil {
					mustUnmarshal(t, f.Message, v)
				}
				var err error
				got, err = MarshalResult(*hdr.Re, v)
				if err != nil {
					t.Fatal(err)
				}
			default:
				v := map[string]func() any{
					TEvent:        func() any { return &Event{} },
					TLagged:       func() any { return &Lagged{} },
					TDetached:     func() any { return &Detached{} },
					TEventsLagged: func() any { return &EventsLagged{} },
					TTranscript:   func() any { return &Transcript{} },
				}[hdr.T]()
				mustUnmarshal(t, f.Message, v)
				var err error
				got, err = json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
			}
			assertJSONEqual(t, f.Message, got)
		})
	}
}

func TestBinaryFixtures(t *testing.T) {
	var fixtures []binaryFixture
	readFixture(t, "binary.json", &fixtures)
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			frame, _ := hex.DecodeString(f.FrameHex)
			payload, _ := hex.DecodeString(f.PayloadHex)
			got, err := DecodeFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			if got.Channel != f.Channel || !bytes.Equal(got.Payload, payload) {
				t.Fatalf("decoded %+v", got)
			}
			var enc []byte
			switch f.Op {
			case "output":
				if got.Op != OpOutput || got.Offset != f.Offset {
					t.Fatalf("decoded %+v", got)
				}
				enc = AppendOutput(nil, f.Channel, f.Offset, payload)
			case "input":
				if got.Op != OpInput {
					t.Fatalf("decoded %+v", got)
				}
				enc = AppendInput(nil, f.Channel, payload)
			}
			if !bytes.Equal(enc, frame) {
				t.Fatalf("encoded %x, want %s", enc, f.FrameHex)
			}
		})
	}
}

func TestDecodeFrameRejects(t *testing.T) {
	cases := map[string][]byte{
		"empty":          {},
		"unknown op":     {0x09, 0, 0, 0, 1},
		"short input":    {OpInput, 0, 0, 1},
		"short output":   {OpOutput, 0, 0, 0, 1, 0, 0, 0},
		"oversize input": append([]byte{OpInput, 0, 0, 0, 1}, make([]byte, MaxPayload+1)...),
	}
	for name, b := range cases {
		if _, err := DecodeFrame(b); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestMarshalResultEmpty(t *testing.T) {
	for _, v := range []any{nil, struct{}{}} {
		b, err := MarshalResult(3, v)
		if err != nil || string(b) != `{"t":"res","re":3,"ok":true}` {
			t.Fatalf("got %s, %v", b, err)
		}
	}
	if _, err := MarshalResult(1, []int{1}); err == nil {
		t.Fatal("non-object result accepted")
	}
}

func TestFormatTime(t *testing.T) {
	ts := time.Date(2026, 10, 27, 21, 0, 0, 123456789, time.FixedZone("CET", 3600))
	if got := FormatTime(ts); got != "2026-10-27T20:00:00.123Z" {
		t.Fatal(got)
	}
}

func mustUnmarshal(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func mergeObject(t *testing.T, base map[string]any, v any) []byte {
	t.Helper()
	b, _ := json.Marshal(v)
	var m map[string]any
	mustUnmarshal(t, b, &m)
	for k, x := range base {
		m[k] = x
	}
	out, _ := json.Marshal(m)
	return out
}

// assertJSONEqual compares two JSON documents, treating a null field and a
// missing field as the same.
func assertJSONEqual(t *testing.T, want, got []byte) {
	t.Helper()
	var w, g any
	mustUnmarshal(t, want, &w)
	mustUnmarshal(t, got, &g)
	if w, g = dropNulls(w), dropNulls(g); !reflect.DeepEqual(w, g) {
		t.Fatalf("round trip mismatch\nwant %s\ngot  %s", want, got)
	}
}

func dropNulls(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if e == nil {
				delete(x, k)
			} else {
				x[k] = dropNulls(e)
			}
		}
	case []any:
		for i := range x {
			x[i] = dropNulls(x[i])
		}
	}
	return v
}
