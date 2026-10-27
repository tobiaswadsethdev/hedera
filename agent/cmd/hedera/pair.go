package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/client"
	"github.com/tobiaswadsethdev/hedera/agent/internal/pairing"
)

func cmdPair(args []string) error {
	fs := newFlags("pair", usageFor("pair"))
	noQR := fs.Bool("no-qr", false, "print only the pairing URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var nc struct {
		CodeID    string `json:"code_id"`
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := localAPI(ctx, "POST", "/v1/local/pair-codes", &nc); err != nil {
		return err
	}
	if !*noQR {
		qrText, err := renderQR(nc.URL)
		if err != nil {
			return err
		}
		fmt.Print(qrText)
	}
	fmt.Printf("\nScan with the Hedera app, or on another machine run:\n\n  hedera remote add '%s'\n\n", nc.URL)
	fmt.Printf("The code works once and expires in %d minutes. Waiting…\n", int(pairing.CodeTTL.Minutes()))
	for {
		var st pairing.CodeStatus
		if err := localAPI(ctx, "GET", "/v1/local/pair-codes/"+nc.CodeID+"/wait", &st); err != nil {
			if ctx.Err() != nil {
				fmt.Println("\ncancelled")
				return nil
			}
			return err
		}
		switch st.Status {
		case "completed":
			fmt.Printf("Paired with %q (%s). Revoke with: hedera tokens revoke %s\n", st.DeviceName, st.PairingID, st.PairingID)
			return nil
		case "expired":
			return errors.New("the code expired; run 'hedera pair' again")
		}
	}
}

func cmdTokens(args []string) error {
	ctx, cancel := callCtx()
	defer cancel()
	if len(args) == 0 || args[0] == "list" {
		var res struct {
			Pairings []struct {
				ID         string `json:"id"`
				DeviceName string `json:"device_name"`
				CreatedAt  string `json:"created_at"`
				LastSeenAt string `json:"last_seen_at"`
			} `json:"pairings"`
		}
		if err := localAPI(ctx, "GET", "/v1/local/pairings", &res); err != nil {
			return err
		}
		if len(res.Pairings) == 0 {
			fmt.Println("no paired devices; pair one with 'hedera pair'")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tDEVICE\tPAIRED\tLAST SEEN")
		for _, p := range res.Pairings {
			seen := "never"
			if p.LastSeenAt != "" {
				seen = age(p.LastSeenAt) + " ago"
			}
			fmt.Fprintf(w, "%s\t%s\t%s ago\t%s\n", p.ID, p.DeviceName, age(p.CreatedAt), seen)
		}
		return w.Flush()
	}
	if args[0] == "revoke" && len(args) == 2 {
		if err := localAPI(ctx, "DELETE", "/v1/local/pairings/"+url.PathEscape(args[1]), nil); err != nil {
			return err
		}
		fmt.Printf("revoked %s; its connections were closed\n", args[1])
		return nil
	}
	return fmt.Errorf("usage: hedera tokens %s", usageFor("tokens"))
}

func cmdRemote(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: hedera remote %s", usageFor("remote"))
	}
	switch args[0] {
	case "add":
		return remoteAdd(args[1:])
	case "ls":
		rs, err := loadRemotes()
		if err != nil {
			return err
		}
		if len(rs) == 0 {
			fmt.Println("no remotes; add one with 'hedera remote add <pairing URL>'")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tURL\tMACHINE")
		for _, r := range rs {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Name, r.URL, r.MachineID)
		}
		return w.Flush()
	case "rm":
		if len(args) != 2 {
			return errors.New("usage: hedera remote rm <name>")
		}
		rs, err := loadRemotes()
		if err != nil {
			return err
		}
		for i, r := range rs {
			if r.Name == args[1] {
				if err := saveRemotes(append(rs[:i], rs[i+1:]...)); err != nil {
					return err
				}
				fmt.Printf("removed %s locally; also run 'hedera tokens revoke %s' on that machine\n", r.Name, r.PairingID)
				return nil
			}
		}
		return fmt.Errorf("no remote named %q", args[1])
	}
	return fmt.Errorf("usage: hedera remote %s", usageFor("remote"))
}

type pairURL struct {
	machineID, name, port, fp, code string
	hosts                           []string
}

func parsePairURL(raw string) (pairURL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "hedera" || u.Host != "pair" {
		return pairURL{}, errors.New("not a hedera://pair URL")
	}
	q := u.Query()
	if q.Get("v") != "1" {
		return pairURL{}, fmt.Errorf("unsupported pairing URL version %q", q.Get("v"))
	}
	p := pairURL{machineID: q.Get("m"), name: q.Get("n"), port: q.Get("p"), fp: q.Get("fp"), code: q.Get("c")}
	p.hosts = append(p.hosts, q["a"]...)
	if d := q.Get("d"); d != "" {
		p.hosts = append(p.hosts, d)
	}
	if p.port == "" || p.fp == "" || p.code == "" || len(p.hosts) == 0 {
		return pairURL{}, errors.New("pairing URL is missing fields")
	}
	return p, nil
}

func remoteAdd(args []string) error {
	fs := newFlags("remote add", "<hedera://pair…> [--name N]")
	name := fs.String("name", "", "name for this remote (default: the machine's hostname)")
	positional, _, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		fs.Usage()
		return errors.New("need the pairing URL printed by 'hedera pair'")
	}
	p, err := parsePairURL(positional[0])
	if err != nil {
		return err
	}
	if *name == "" {
		*name = p.name
	}
	rs, err := loadRemotes()
	if err != nil {
		return err
	}
	for _, r := range rs {
		if r.Name == *name {
			return fmt.Errorf("a remote named %q exists; pick another with --name or 'hedera remote rm' it", *name)
		}
	}
	hostname, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{"code": p.code, "device_name": "hedera CLI on " + hostname})
	hc := client.HTTPClient(p.fp)
	hc.Timeout = 10 * time.Second
	var lastErr error
	for _, host := range p.hosts {
		base := "https://" + net.JoinHostPort(host, p.port)
		resp, err := hc.Post(base+"/v1/pair", "application/json", bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("pairing refused: %s %s", resp.Status, strings.TrimSpace(string(b)))
		}
		var out struct {
			PairingID string `json:"pairing_id"`
			Token     string `json:"token"`
			MachineID string `json:"machine_id"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return err
		}
		if p.machineID != "" && out.MachineID != p.machineID {
			return errors.New("the agent's machine ID does not match the pairing URL")
		}
		r := Remote{
			Name:        *name,
			URL:         "wss://" + net.JoinHostPort(host, p.port) + "/v1/ws",
			Token:       out.Token,
			Fingerprint: p.fp,
			MachineID:   out.MachineID,
			PairingID:   out.PairingID,
		}
		if err := saveRemotes(append(rs, r)); err != nil {
			return err
		}
		fmt.Printf("paired with %s as %q; try: hedera ls --remote %s\n", host, *name, *name)
		return nil
	}
	return fmt.Errorf("could not reach the agent at any address in the URL: %v", lastErr)
}
