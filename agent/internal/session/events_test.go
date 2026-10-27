package session

import (
	"context"
	"testing"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func publishN(b *Bus, n int) {
	for i := 0; i < n; i++ {
		b.Publish(proto.Event{Type: proto.EvSessionRemoved, SessionID: "s_aaaaaaaaaa"})
	}
}

func u64(v uint64) *uint64 { return &v }

func TestBusSequenceAndBacklog(t *testing.T) {
	b := NewBus(5, 10, nil)
	publishN(b, 3)
	sub, backlog, seq, reset := b.Subscribe(u64(1))
	if seq != 3 || reset || len(backlog) != 2 || backlog[0].Seq != 2 || backlog[1].Seq != 3 {
		t.Fatalf("seq %d reset %v backlog %+v", seq, reset, backlog)
	}
	if backlog[0].T != proto.TEvent || backlog[0].TS == "" {
		t.Fatalf("event not stamped: %+v", backlog[0])
	}
	publishN(b, 1)
	if ev := <-sub.C; ev.Seq != 4 {
		t.Fatalf("live seq %d", ev.Seq)
	}
}

func TestBusSubscribeEdges(t *testing.T) {
	b := NewBus(5, 10, nil)
	publishN(b, 8) // log holds 4..8

	cases := []struct {
		name     string
		since    *uint64
		reset    bool
		backlogN int
		firstSeq uint64
	}{
		{"no since", nil, false, 0, 0},
		{"current", u64(8), false, 0, 0},
		{"oldest boundary", u64(3), false, 5, 4},
		{"too old", u64(2), true, 0, 0},
		{"from another boot", u64(50), true, 0, 0},
	}
	for _, c := range cases {
		_, backlog, seq, reset := b.Subscribe(c.since)
		if seq != 8 || reset != c.reset || len(backlog) != c.backlogN {
			t.Errorf("%s: seq %d reset %v backlog %d", c.name, seq, reset, len(backlog))
			continue
		}
		if c.backlogN > 0 && backlog[0].Seq != c.firstSeq {
			t.Errorf("%s: first %d", c.name, backlog[0].Seq)
		}
	}
}

func TestBusSlowSubscriberIsDropped(t *testing.T) {
	b := NewBus(100, 3, nil)
	sub, _, _, _ := b.Subscribe(nil)
	publishN(b, 5)
	var got []uint64
	for ev := range sub.C {
		got = append(got, ev.Seq)
	}
	if len(got) != 3 || got[2] != 3 || !b.Lagged(sub) {
		t.Fatalf("got %v lagged %v", got, b.Lagged(sub))
	}
	b.Unsubscribe(sub) // already closed: must not panic
}

func TestBusUnsubscribe(t *testing.T) {
	b := NewBus(10, 10, nil)
	sub, _, _, _ := b.Subscribe(nil)
	b.Unsubscribe(sub)
	publishN(b, 1)
	if _, ok := <-sub.C; ok {
		t.Fatal("received after unsubscribe")
	}
	if b.Lagged(sub) {
		t.Fatal("unsubscribe reported as lag")
	}
}
