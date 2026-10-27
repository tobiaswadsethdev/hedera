package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
)

func cmdList(args []string) error {
	fs := newFlags("ls", usageFor("ls"))
	remote := targetFlag(fs)
	asJSON := fs.Bool("json", false, "print the raw session objects")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := dial(*remote)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, cancel := callCtx()
	defer cancel()
	var res proto.ListResult
	if err := c.Call(ctx, proto.TSessionList, nil, &res); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res.Sessions)
	}
	if len(res.Sessions) == 0 {
		fmt.Println("no sessions")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tSTATUS\tATTACHED\tAGE\tTITLE")
	for _, s := range res.Sessions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", s.ID, s.Kind, statusText(s), s.Attached, age(s.CreatedAt), s.Title)
	}
	return w.Flush()
}

func statusText(s proto.Session) string {
	if s.Exit == nil {
		return s.Status
	}
	switch {
	case s.Exit.Signal != nil:
		return "exited (" + *s.Exit.Signal + ")"
	case s.Exit.Code != nil:
		return fmt.Sprintf("exited (%d)", *s.Exit.Code)
	}
	return "exited"
}

func age(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return "?"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return d.String()
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func cmdNew(args []string) error {
	fs := newFlags("new", usageFor("new"))
	remote := targetFlag(fs)
	claude := fs.Bool("claude", false, "start a Claude Code session instead of a shell")
	cwd := fs.String("cwd", "", "working directory (default: your home directory; ~ is expanded on the agent)")
	title := fs.String("title", "", "session title")
	detached := fs.Bool("d", false, "don't attach after creating")
	positional, rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	argv := append(positional, rest...)
	req := proto.CreateRequest{Kind: proto.KindShell, Cwd: *cwd, Argv: argv, Title: *title}
	if *claude {
		req.Kind = proto.KindClaude
	}
	if req.Cwd == "" && *remote == "" {
		// Locally, default to where the user is, like a new terminal tab.
		req.Cwd, _ = os.Getwd()
	}
	tty := term.IsTerminal(int(os.Stdin.Fd()))
	if tty {
		if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			req.Cols, req.Rows = w, h
		}
	}
	c, err := dial(*remote)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx()
	var res proto.CreateResult
	err = c.Call(ctx, proto.TSessionCreate, req, &res)
	cancel()
	if err != nil {
		c.Close()
		return err
	}
	if *detached || !tty {
		fmt.Println(res.Session.ID)
		return c.Close()
	}
	return attachLoop(c, res.Session.ID, nil)
}

func cmdAttach(args []string) error {
	fs := newFlags("attach", usageFor("attach"))
	remote := targetFlag(fs)
	from := fs.Int64("from", -1, "replay from this output offset (default: the whole scrollback)")
	positional, _, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		fs.Usage()
		return errors.New("need exactly one session ID")
	}
	var fromOff *uint64
	if *from >= 0 {
		v := uint64(*from)
		fromOff = &v
	}
	c, err := dial(*remote)
	if err != nil {
		return err
	}
	return attachLoop(c, positional[0], fromOff)
}

func cmdClose(args []string) error {
	fs := newFlags("close", usageFor("close"))
	remote := targetFlag(fs)
	kill := fs.Bool("kill", false, "SIGKILL immediately instead of hanging up")
	positional, _, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		fs.Usage()
		return errors.New("need at least one session ID")
	}
	mode := "hangup"
	if *kill {
		mode = "kill"
	}
	return eachSession(*remote, positional, func(call func(string, any) error, id string) error {
		return call(proto.TSessionClose, proto.CloseRequest{SessionID: id, Mode: mode})
	})
}

func cmdRemove(args []string) error {
	fs := newFlags("rm", usageFor("rm"))
	remote := targetFlag(fs)
	positional, _, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		fs.Usage()
		return errors.New("need at least one session ID")
	}
	return eachSession(*remote, positional, func(call func(string, any) error, id string) error {
		return call(proto.TSessionRemove, proto.RemoveRequest{SessionID: id})
	})
}

func eachSession(remote string, ids []string, fn func(call func(string, any) error, id string) error) error {
	c, err := dial(remote)
	if err != nil {
		return err
	}
	defer c.Close()
	call := func(t string, params any) error {
		ctx, cancel := callCtx()
		defer cancel()
		return c.Call(ctx, t, params, nil)
	}
	var failed []string
	for _, id := range ids {
		if err := fn(call, id); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", id, err)
			failed = append(failed, id)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed for %s", strings.Join(failed, ", "))
	}
	return nil
}

func cmdEvents(args []string) error {
	fs := newFlags("events", usageFor("events"))
	remote := targetFlag(fs)
	since := fs.Int64("since", -1, "replay events after this sequence number")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := dial(*remote)
	if err != nil {
		return err
	}
	defer c.Close()
	req := proto.SubscribeRequest{}
	if *since >= 0 {
		v := uint64(*since)
		req.SinceSeq = &v
	}
	ctx, cancel := callCtx()
	var res proto.SubscribeResult
	err = c.Call(ctx, proto.TEventsSubscribe, req, &res)
	cancel()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "subscribed at seq %d (boot %s)%s\n", res.Seq, c.Hello.BootID, map[bool]string{true: "; history unavailable, reset", false: ""}[res.Reset])
	for m := range c.Messages {
		if m.T == proto.TEvent || m.T == proto.TEventsLagged {
			fmt.Println(string(m.Raw))
		}
	}
	return c.Err()
}
