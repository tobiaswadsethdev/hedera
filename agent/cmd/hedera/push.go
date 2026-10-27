package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/tobiaswadsethdev/hedera/agent/internal/proto"
	"github.com/tobiaswadsethdev/hedera/agent/internal/push"
)

func cmdPush(args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	switch args[0] {
	case "status":
		return pushStatus()
	case "test":
		ctx, cancel := callCtx()
		defer cancel()
		var res struct {
			Queued int `json:"queued"`
		}
		if err := localAPI(ctx, "POST", "/v1/local/push/test", &res); err != nil {
			return err
		}
		if res.Queued == 0 {
			return errors.New("no working push routes; pair the app, or use 'hedera push configure'")
		}
		fmt.Printf("test notification queued for %d device(s); see 'hedera push status' for the result\n", res.Queued)
		return nil
	case "configure":
		return pushConfigure(args[1:])
	case "remove":
		c, err := dial("")
		if err != nil {
			return err
		}
		defer c.Close()
		ctx, cancel := callCtx()
		defer cancel()
		if err := c.Call(ctx, proto.TPushRemove, nil, nil); err != nil {
			return err
		}
		fmt.Println("removed this machine's local push route")
		return nil
	}
	return fmt.Errorf("usage: hedera push %s", usageFor("push"))
}

func pushStatus() error {
	ctx, cancel := callCtx()
	defer cancel()
	var res struct {
		Routes []push.RouteStatus `json:"routes"`
	}
	if err := localAPI(ctx, "GET", "/v1/local/push", &res); err != nil {
		return err
	}
	if len(res.Routes) == 0 {
		fmt.Println("push is not configured; the Hedera app sets it up when you pair")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ROUTE\tDEVICE\tRADIX\tQUEUED\tLAST SENT\tSTATE")
	for _, r := range res.Routes {
		sent := "never"
		if r.LastSuccessAt != "" {
			sent = age(r.LastSuccessAt) + " ago"
		}
		state := "ok"
		switch {
		case r.Broken != "":
			state = r.Broken
		case r.LastError != "":
			state = "retrying: " + r.LastError
		}
		var off []string
		for _, k := range push.PrefKinds {
			if !r.Prefs[k] {
				off = append(off, k)
			}
		}
		if len(off) > 0 {
			state += " (off: " + strings.Join(off, ", ") + ")"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", r.Key, r.Label, r.RadixURL, r.Queued, sent, state)
	}
	return w.Flush()
}

func pushConfigure(args []string) error {
	fs := newFlags("push configure", "--radix URL --token hrp_… [--off kind,kind]")
	radix := fs.String("radix", "", "Radix base URL, e.g. https://radix.example.workers.dev")
	token := fs.String("token", "", "push token from Radix's POST /v1/machines")
	off := fs.String("off", "", "comma-separated kinds not to notify: "+strings.Join(push.PrefKinds, ", "))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *radix == "" || *token == "" {
		fs.Usage()
		return errors.New("--radix and --token are required")
	}
	prefs := map[string]bool{}
	for _, k := range strings.Split(*off, ",") {
		if k = strings.TrimSpace(k); k != "" {
			prefs[k] = false
		}
	}
	c, err := dial("")
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, cancel := callCtx()
	defer cancel()
	var st proto.PushStatus
	if err := c.Call(ctx, proto.TPushConfigure, proto.PushConfigureRequest{RadixURL: *radix, PushToken: *token, Prefs: prefs}, &st); err != nil {
		return err
	}
	fmt.Printf("push configured for %q via %s; try 'hedera push test'\n", st.Label, st.RadixURL)
	return nil
}
