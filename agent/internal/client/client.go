// Package client is a Go client for the Hedera agent protocol. The CLI uses
// it for `hedera attach`, `ls` and friends, and the server tests use it as the
// reference client.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/tlsutil"
)

// Options says how to reach an agent.
type Options struct {
	URL         string // ws://127.0.0.1:7374/v1/ws or wss://100.x.y.z:7373/v1/ws
	Token       string
	Fingerprint string // required for wss://
	ClientName  string
	Version     string
}

// Message is anything the agent sends that is not a response: an OUTPUT
// frame, or a lagged/detached/event/events.lagged control message. Messages
// arrive in the order the agent sent them.
type Message struct {
	Output *proto.Frame    // set for OUTPUT frames
	T      string          // control message type, when Output is nil
	Raw    json.RawMessage // the control message
}

// Client is one WebSocket connection to an agent.
type Client struct {
	ws     *websocket.Conn
	nextID atomic.Uint64
	Hello  proto.HelloResult

	// Messages delivers unsolicited messages. If nobody reads it, the
	// connection stalls and the agent eventually reports lag.
	Messages <-chan Message
	msgs     chan Message

	mu      sync.Mutex
	pending map[uint64]chan json.RawMessage
	err     error
	done    chan struct{}
}

// HTTPClient returns an HTTP client for the agent: pinned TLS when fp is set.
func HTTPClient(fp string) *http.Client {
	if fp == "" {
		return &http.Client{}
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsutil.PinnedClientConfig(fp)}}
}

// Dial connects and performs the hello handshake.
func Dial(ctx context.Context, o Options) (*Client, error) {
	if strings.HasPrefix(o.URL, "wss://") && o.Fingerprint == "" {
		return nil, errors.New("client: wss:// needs a fingerprint to pin")
	}
	ws, resp, err := websocket.Dial(ctx, o.URL, &websocket.DialOptions{
		HTTPClient:   HTTPClient(o.Fingerprint),
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + o.Token}},
		Subprotocols: []string{proto.Subprotocol},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	ws.SetReadLimit(proto.MaxTextMessage)
	msgs := make(chan Message, 1024)
	c := &Client{
		ws:       ws,
		Messages: msgs,
		msgs:     msgs,
		pending:  map[uint64]chan json.RawMessage{},
		done:     make(chan struct{}),
	}
	go c.readLoop()
	name, version := o.ClientName, o.Version
	if name == "" {
		name = "hedera-cli"
	}
	if version == "" {
		version = "dev"
	}
	err = c.Call(ctx, proto.THello, proto.Hello{
		Protocol: proto.VersionRange{Min: proto.MinVersion, Max: proto.Version},
		Client:   proto.ClientInfo{Name: name, Version: version},
	}, &c.Hello)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	return c, nil
}

// ErrUnauthorized means the agent rejected the token.
var ErrUnauthorized = errors.New("client: agent rejected the credentials (revoked or wrong token?)")

func (c *Client) readLoop() {
	defer close(c.done)
	defer close(c.msgs)
	for {
		typ, data, err := c.ws.Read(context.Background())
		if err != nil {
			c.mu.Lock()
			c.err = err
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		if typ == websocket.MessageBinary {
			f, err := proto.DecodeFrame(data)
			if err != nil || f.Op != proto.OpOutput {
				continue
			}
			c.msgs <- Message{Output: &f}
			continue
		}
		var h struct {
			T  string  `json:"t"`
			Re *uint64 `json:"re"`
		}
		if json.Unmarshal(data, &h) != nil {
			continue
		}
		if h.T == proto.TResponse && h.Re != nil {
			c.mu.Lock()
			ch := c.pending[*h.Re]
			delete(c.pending, *h.Re)
			c.mu.Unlock()
			if ch != nil {
				ch <- data
			}
			continue
		}
		c.msgs <- Message{T: h.T, Raw: data}
	}
}

// Call sends a request and decodes the success fields into result (which may
// be nil). A failure response is returned as *proto.Error.
func (c *Client) Call(ctx context.Context, t string, params, result any) error {
	id := c.nextID.Add(1)
	b, err := proto.MarshalRequest(t, id, params)
	if err != nil {
		return err
	}
	ch := make(chan json.RawMessage, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		return err
	}
	select {
	case raw, ok := <-ch:
		if !ok {
			return c.Err()
		}
		var r proto.Response
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if !r.OK {
			if r.Error == nil {
				return fmt.Errorf("client: %s failed without an error", t)
			}
			return r.Error
		}
		if result != nil {
			return json.Unmarshal(raw, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Input sends an INPUT frame on an attached channel.
func (c *Client) Input(ctx context.Context, channel uint32, data []byte) error {
	for len(data) > 0 {
		n := min(len(data), proto.MaxPayload)
		if err := c.ws.Write(ctx, websocket.MessageBinary, proto.AppendInput(nil, channel, data[:n])); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// Done is closed when the connection ends.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is why the connection ended.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// CloseStatus is the WebSocket close code the agent sent, or -1.
func (c *Client) CloseStatus() websocket.StatusCode {
	return websocket.CloseStatus(c.Err())
}

func (c *Client) Close() error {
	return c.ws.Close(websocket.StatusNormalClosure, "")
}
