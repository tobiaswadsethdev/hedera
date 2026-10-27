// Package pairing stores paired clients and issues one-time pairing codes.
//
// Only SHA-256 hashes of tokens and codes are kept, and every comparison is
// constant-time.
package pairing

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/config"
	"github.com/tobiaswadsethdev/hedera/agent/internal/ids"
)

const (
	CodeTTL       = 5 * time.Minute
	maxDeviceName = 100
	touchInterval = time.Minute
)

var (
	ErrBadCode  = errors.New("pairing: unknown, used or expired code")
	ErrNotFound = errors.New("pairing: no such pairing")
)

// Pairing is one paired client.
type Pairing struct {
	ID         string    `json:"id"`
	DeviceName string    `json:"device_name"`
	TokenHash  string    `json:"token_hash"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at,omitzero"`
}

// CodeStatus is the state of a pairing code.
type CodeStatus struct {
	Status     string `json:"status"` // pending, completed, expired
	PairingID  string `json:"pairing_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

type code struct {
	id      string
	hash    [32]byte
	expires time.Time
	used    bool
	result  CodeStatus
	done    chan struct{} // closed when completed
}

type Store struct {
	path string
	now  func() time.Time

	mu       sync.Mutex
	pairings []Pairing
	codes    map[string]*code
	dirty    time.Time // last persisted LastSeenAt flush
}

// Open loads pairings from path (a missing file means none).
func Open(path string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	s := &Store{path: path, now: now, codes: map[string]*code{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &s.pairings); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func hash(secret string) [32]byte { return sha256.Sum256([]byte(secret)) }

// Authenticate returns the pairing that token belongs to. Every stored hash
// is compared, in constant time, whether or not an earlier one matched.
func (s *Store) Authenticate(token string) (string, bool) {
	h := hash(token)
	want := hex.EncodeToString(h[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	match := -1
	for i, p := range s.pairings {
		if subtle.ConstantTimeCompare([]byte(p.TokenHash), []byte(want)) == 1 {
			match = i
		}
	}
	if match < 0 {
		return "", false
	}
	p := &s.pairings[match]
	now := s.now()
	p.LastSeenAt = now
	if now.Sub(s.dirty) > touchInterval {
		s.dirty = now
		s.saveLocked()
	}
	return p.ID, true
}

// NewCode issues a one-time pairing code valid for CodeTTL.
func (s *Store) NewCode() (codeID, secret string, expires time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	secret = ids.Code()
	c := &code{
		id:      ids.New("c_", 10),
		hash:    hash(secret),
		expires: s.now().Add(CodeTTL),
		result:  CodeStatus{Status: "pending"},
		done:    make(chan struct{}),
	}
	s.codes[c.id] = c
	return c.id, secret, c.expires
}

// Redeem exchanges a pairing code for a new long-lived token.
func (s *Store) Redeem(secret, deviceName string) (Pairing, string, error) {
	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" {
		deviceName = "unnamed device"
	}
	if r := []rune(deviceName); len(r) > maxDeviceName {
		deviceName = string(r[:maxDeviceName])
	}
	h := hash(secret)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var found *code
	for _, c := range s.codes {
		if subtle.ConstantTimeCompare(c.hash[:], h[:]) == 1 {
			found = c
		}
	}
	if found == nil || found.used || now.After(found.expires) {
		return Pairing{}, "", ErrBadCode
	}
	found.used = true
	token := ids.Secret("hdt_")
	th := hash(token)
	p := Pairing{
		ID:         ids.PairingID(),
		DeviceName: deviceName,
		TokenHash:  hex.EncodeToString(th[:]),
		CreatedAt:  now,
	}
	s.pairings = append(s.pairings, p)
	if err := s.saveLocked(); err != nil {
		s.pairings = s.pairings[:len(s.pairings)-1]
		found.used = false
		return Pairing{}, "", err
	}
	found.result = CodeStatus{Status: "completed", PairingID: p.ID, DeviceName: p.DeviceName}
	close(found.done)
	return p, token, nil
}

// WaitCode blocks until the code is redeemed, expires, or ctx ends, and
// reports its status.
func (s *Store) WaitCode(ctx context.Context, codeID string) (CodeStatus, error) {
	s.mu.Lock()
	c, ok := s.codes[codeID]
	s.mu.Unlock()
	if !ok {
		return CodeStatus{}, ErrNotFound
	}
	timer := time.NewTimer(c.expires.Sub(s.now()))
	defer timer.Stop()
	select {
	case <-c.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.result.Status == "pending" && s.now().After(c.expires) {
		return CodeStatus{Status: "expired"}, nil
	}
	return c.result, nil
}

// List returns all pairings, without token hashes.
func (s *Store) List() []Pairing {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Pairing, len(s.pairings))
	for i, p := range s.pairings {
		p.TokenHash = ""
		out[i] = p
	}
	return out
}

// Revoke deletes a pairing. The caller closes its live connections.
func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.pairings {
		if p.ID == id {
			s.pairings = append(s.pairings[:i:i], s.pairings[i+1:]...)
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

// pruneLocked drops codes that expired a while ago.
func (s *Store) pruneLocked() {
	cutoff := s.now().Add(-CodeTTL)
	for id, c := range s.codes {
		if c.expires.Before(cutoff) {
			delete(s.codes, id)
		}
	}
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.pairings, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.path, append(b, '\n'), 0o600)
}
