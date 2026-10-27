package session

import (
	"sync"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

const (
	defaultEventLog   = 1000
	defaultEventQueue = 256
)

// Bus assigns sequence numbers to events, keeps a bounded log for
// resubscription, and fans events out to subscribers.
type Bus struct {
	mu    sync.Mutex
	seq   uint64
	log   []proto.Event // ring of the last cap(log) events
	start int           // index of the oldest event in log
	subs  map[*EventSub]struct{}
	queue int
	now   func() time.Time
}

// EventSub receives events on C. When the subscriber falls behind, C is
// closed after the events already queued; Lagged then reports true.
type EventSub struct {
	C      <-chan proto.Event
	ch     chan proto.Event
	lagged bool
	closed bool
}

func NewBus(logSize, queue int, now func() time.Time) *Bus {
	if logSize <= 0 {
		logSize = defaultEventLog
	}
	if queue <= 0 {
		queue = defaultEventQueue
	}
	if now == nil {
		now = time.Now
	}
	return &Bus{log: make([]proto.Event, 0, logSize), subs: map[*EventSub]struct{}{}, queue: queue, now: now}
}

// Seq returns the sequence number of the latest event.
func (b *Bus) Seq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// Publish stamps ev with the next sequence number and time and delivers it.
func (b *Bus) Publish(ev proto.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	ev.T = proto.TEvent
	ev.Seq = b.seq
	ev.TS = proto.FormatTime(b.now())
	if len(b.log) < cap(b.log) {
		b.log = append(b.log, ev)
	} else {
		b.log[b.start] = ev
		b.start = (b.start + 1) % len(b.log)
	}
	for s := range b.subs {
		select {
		case s.ch <- ev:
		default:
			s.lagged = true
			b.closeLocked(s)
		}
	}
}

// Subscribe registers a subscriber. With since set, it returns the logged
// events after since as backlog; if any of them are gone, reset is true and
// backlog is empty. Registration and backlog are atomic with respect to
// Publish, so no event is missed or duplicated.
func (b *Bus) Subscribe(since *uint64) (sub *EventSub, backlog []proto.Event, seq uint64, reset bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan proto.Event, b.queue)
	sub = &EventSub{C: ch, ch: ch}
	b.subs[sub] = struct{}{}
	seq = b.seq
	if since == nil || *since == b.seq {
		return sub, nil, seq, false
	}
	oldest := b.seq - uint64(len(b.log)) // events after oldest are logged
	if *since > b.seq || *since < oldest {
		return sub, nil, seq, true
	}
	for i := 0; i < len(b.log); i++ {
		ev := b.log[(b.start+i)%len(b.log)]
		if ev.Seq > *since {
			backlog = append(backlog, ev)
		}
	}
	return sub, backlog, seq, false
}

// Unsubscribe removes sub and closes its channel. It is safe to call more
// than once.
func (b *Bus) Unsubscribe(sub *EventSub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeLocked(sub)
}

func (b *Bus) closeLocked(s *EventSub) {
	if s.closed {
		return
	}
	s.closed = true
	delete(b.subs, s)
	close(s.ch)
}

// Lagged reports whether the subscription ended because the subscriber fell
// behind. Only meaningful after C is closed.
func (b *Bus) Lagged(s *EventSub) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return s.lagged
}
