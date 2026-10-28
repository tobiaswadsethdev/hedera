package server

import (
	"encoding/json"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/transcript"
)

const (
	defaultTail     = 200
	maxTail         = 1000
	defaultReadSize = 100
	// transcriptPoll is how often a subscription looks for new entries.
	// Claude Code writes the file as it goes; there is no hook per message.
	transcriptPoll = 300 * time.Millisecond
	// transcriptBatch bounds one transcript message, well below the
	// client's 1 MiB limit for text messages.
	transcriptBatch = 256 << 10
	// transcriptStep is how many entries one After call returns.
	transcriptStep = 200
)

// transcriptStream is one transcript.subscribe on a connection.
type transcriptStream struct {
	stop chan struct{}
	done chan struct{}
}

func (c *conn) handleTranscriptSubscribe(data []byte) (any, func(), error) {
	var req proto.TranscriptSubscribeRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	tr, err := c.transcriptOf(req.SessionID)
	if err != nil {
		return nil, nil, err
	}
	c.stopTranscript(req.SessionID)
	tr.Refresh()
	first, last := tr.Seqs()
	res := proto.TranscriptSubscribeResult{Seq: last, FirstSeq: first}
	var backlog []proto.TranscriptEntry
	from := last
	switch {
	case req.Since != nil && *req.Since > last:
		// Numbers from another boot or session: the client starts over.
		res.Reset = true
		return res, nil, nil
	case req.Since != nil:
		from = *req.Since
	default:
		n := defaultTail
		if req.Tail != nil {
			n = min(max(*req.Tail, 0), maxTail)
		}
		backlog = tr.Tail(n)
	}
	ts := &transcriptStream{stop: make(chan struct{}), done: make(chan struct{})}
	c.mu.Lock()
	c.transcripts[req.SessionID] = ts
	c.mu.Unlock()
	return res, func() { go c.runTranscript(req.SessionID, tr, ts, backlog, from) }, nil
}

func (c *conn) transcriptOf(id string) (*transcript.Transcript, error) {
	s, err := c.srv.Manager.Get(id)
	if err != nil {
		return nil, err
	}
	tr := s.Transcript()
	if tr == nil || !tr.Ready() {
		return nil, proto.Errorf(proto.CodeUnavailable, "session %s has no transcript yet", id)
	}
	return tr, nil
}

// runTranscript sends the backlog, then new entries as they are read. Sends
// block until the client takes them, so a slow client slows the stream down
// rather than losing entries.
func (c *conn) runTranscript(id string, tr *transcript.Transcript, ts *transcriptStream, backlog []proto.TranscriptEntry, last uint64) {
	defer close(ts.done)
	if !c.sendTranscript(id, backlog) {
		return
	}
	tick := time.NewTicker(transcriptPoll)
	defer tick.Stop()
	for {
		changed := tr.Changed()
		entries := tr.After(last, transcriptStep)
		if len(entries) > 0 {
			if !c.sendTranscript(id, entries) {
				return
			}
			last = entries[len(entries)-1].Seq
			if len(entries) == transcriptStep {
				continue
			}
		}
		select {
		case <-ts.stop:
			return
		case <-c.ctx.Done():
			return
		case <-changed:
		case <-tick.C:
			tr.Refresh()
		}
	}
}

// sendTranscript sends entries in messages of at most transcriptBatch bytes.
func (c *conn) sendTranscript(id string, entries []proto.TranscriptEntry) bool {
	for len(entries) > 0 {
		n, size := 0, 0
		for n < len(entries) {
			b, _ := json.Marshal(entries[n])
			if n > 0 && size+len(b) > transcriptBatch {
				break
			}
			size += len(b)
			n++
		}
		if c.sendJSON(proto.Transcript{T: proto.TTranscript, SessionID: id, Entries: entries[:n]}) != nil {
			return false
		}
		entries = entries[n:]
	}
	return true
}

func (c *conn) stopTranscript(id string) {
	c.mu.Lock()
	ts := c.transcripts[id]
	delete(c.transcripts, id)
	c.mu.Unlock()
	if ts != nil {
		close(ts.stop)
		<-ts.done
	}
}

func (c *conn) handleTranscriptUnsubscribe(data []byte) (any, func(), error) {
	var req proto.TranscriptUnsubscribeRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	c.stopTranscript(req.SessionID)
	return nil, nil, nil
}

func (c *conn) handleTranscriptRead(data []byte) (any, func(), error) {
	var req proto.TranscriptReadRequest
	if err := decode(data, &req); err != nil {
		return nil, nil, err
	}
	tr, err := c.transcriptOf(req.SessionID)
	if err != nil {
		return nil, nil, err
	}
	n := defaultReadSize
	if req.Limit != nil {
		n = min(max(*req.Limit, 1), maxTail)
	}
	entries := tr.Before(req.Before, n)
	if entries == nil {
		entries = []proto.TranscriptEntry{}
	}
	return proto.TranscriptReadResult{Entries: entries}, nil, nil
}
