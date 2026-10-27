package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/tobiaswadsethdev/hedera/agent/internal/client"
	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

const detachKey = 0x1d // Ctrl-]

// attachLoop connects this terminal to a session until it exits, the user
// detaches, or the connection drops.
func attachLoop(c *client.Client, id string, from *uint64) error {
	defer c.Close()
	inFd, outFd := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	tty := term.IsTerminal(inFd)

	size := func() (int, int) {
		if w, h, err := term.GetSize(outFd); err == nil {
			return w, h
		}
		return 0, 0
	}
	attach := func(from *uint64) (proto.AttachResult, error) {
		req := proto.AttachRequest{SessionID: id, FromOffset: from}
		req.Cols, req.Rows = size()
		ctx, cancel := callCtx()
		defer cancel()
		var res proto.AttachResult
		err := c.Call(ctx, proto.TSessionAttach, req, &res)
		return res, err
	}
	res, err := attach(from)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[attached to %s %q; press Ctrl-] to detach]\r\n", id, res.Session.Title)
	if tty {
		old, err := term.MakeRaw(inFd)
		if err != nil {
			return err
		}
		defer term.Restore(inFd, old)
	}
	if res.Replay.Truncated {
		os.Stdout.Write([]byte("\x1bc"))
	}

	var channel atomic.Uint32
	channel.Store(res.Channel)
	detach := make(chan struct{})
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				data := buf[:n]
				i := bytes.IndexByte(data, detachKey)
				if i >= 0 {
					data = data[:i]
				}
				if len(data) > 0 {
					c.Input(context.Background(), channel.Load(), data)
				}
				if i >= 0 {
					close(detach)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	stopResize := make(chan struct{})
	defer close(stopResize)
	if tty {
		go watchResize(stopResize, func() {
			cols, rows := size()
			if cols > 0 {
				ctx, cancel := callCtx()
				c.Call(ctx, proto.TSessionResize, proto.ResizeRequest{SessionID: id, Cols: cols, Rows: rows}, nil)
				cancel()
			}
		})
	}

	finish := func(msg string) error {
		if tty {
			// Leave the cursor on a fresh line after full-screen programs.
			os.Stdout.Write([]byte("\x1b[0m\r\n"))
		}
		fmt.Fprintf(os.Stderr, "[%s]\r\n", msg)
		return nil
	}
	for {
		select {
		case <-detach:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			c.Call(ctx, proto.TSessionDetach, proto.DetachRequest{Channel: channel.Load()}, nil)
			cancel()
			return finish("detached from " + id)
		case m, ok := <-c.Messages:
			if !ok {
				if tty {
					os.Stdout.Write([]byte("\r\n"))
				}
				return fmt.Errorf("connection lost: %v", c.Err())
			}
			switch {
			case m.Output != nil:
				// Channel IDs only grow, so a newer one is our re-attach after
				// a lag; its frames can arrive before Call returns.
				cur := channel.Load()
				if m.Output.Channel > cur {
					channel.Store(m.Output.Channel)
				} else if m.Output.Channel < cur {
					continue
				}
				os.Stdout.Write(m.Output.Payload)
			case m.T == proto.TLagged:
				var l proto.Lagged
				json.Unmarshal(m.Raw, &l)
				off := l.Offset
				go func() {
					if res, err := attach(&off); err == nil {
						if res.Channel > channel.Load() {
							channel.Store(res.Channel)
						}
					}
				}()
			case m.T == proto.TDetached:
				var d proto.Detached
				json.Unmarshal(m.Raw, &d)
				if d.Channel != channel.Load() {
					continue
				}
				if d.Reason == "removed" {
					return finish("session removed")
				}
				return finish(exitMessage(c, id))
			}
		}
	}
}

func exitMessage(c *client.Client, id string) string {
	ctx, cancel := callCtx()
	defer cancel()
	var res proto.ListResult
	if c.Call(ctx, proto.TSessionList, nil, &res) == nil {
		for _, s := range res.Sessions {
			if s.ID == id {
				return "session " + statusText(s)
			}
		}
	}
	return "session exited"
}
