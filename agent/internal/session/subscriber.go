package session

import (
	"sync"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

// End reasons reported by Subscriber.Take.
const (
	EndExited   = "exited"
	EndRemoved  = "removed"
	EndLagged   = "lagged"
	EndDetached = "detached"
)

// Chunk is a run of output starting at Offset.
type Chunk struct {
	Offset uint64
	Data   []byte
}

// End says why a subscription stopped. For EndLagged, Offset is the first byte
// the subscriber did not get.
type End struct {
	Reason string
	Offset uint64
}

// Subscriber is one attached output stream. The session appends chunks; the
// consumer waits on Ready and drains with Take.
type Subscriber struct {
	mu      sync.Mutex
	pending []Chunk
	queued  int // live bytes pending; replay does not count
	limit   int
	end     *End
	ready   chan struct{}
}

func newSubscriber(limit int) *Subscriber {
	return &Subscriber{limit: limit, ready: make(chan struct{}, 1)}
}

// Ready is signalled whenever Take has something new to return.
func (s *Subscriber) Ready() <-chan struct{} { return s.ready }

func (s *Subscriber) signal() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// Take returns pending chunks and, once all output has been taken, the end of
// the subscription (nil while it is live).
func (s *Subscriber) Take() ([]Chunk, *End) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.pending
	s.pending = nil
	s.queued = 0
	return c, s.end
}

// addReplay queues replay data, split into frame-sized chunks.
func (s *Subscriber) addReplay(off uint64, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(data) > 0 {
		n := min(len(data), proto.MaxPayload)
		s.pending = append(s.pending, Chunk{off, data[:n]})
		off += uint64(n)
		data = data[n:]
	}
	s.signal()
}

// push queues live output. It returns false, and ends the subscription with
// EndLagged, when the consumer is too far behind.
func (s *Subscriber) push(off uint64, data []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end != nil {
		return false
	}
	if s.queued+len(data) > s.limit {
		s.end = &End{Reason: EndLagged, Offset: off}
		s.signal()
		return false
	}
	s.pending = append(s.pending, Chunk{off, data})
	s.queued += len(data)
	s.signal()
	return true
}

func (s *Subscriber) finish(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end == nil {
		s.end = &End{Reason: reason}
	}
	s.signal()
}
