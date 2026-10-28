# Hedera

Control terminals and Claude Code sessions on your computers from an iPhone.

| Part | Path | What it is |
|---|---|---|
| `hedera` | [`agent/`](agent) | Go agent on each computer: owns PTYs, serves the WebSocket API over Tailscale, and is also the CLI |
| Radix | [`relay/`](relay) | Cloudflare Worker that holds the APNs key and delivers push notifications |
| Hedera app | [`ios/`](ios) | SwiftUI app with SwiftTerm terminals; [build notes](ios/README.md) |
| Protocol | [`protocol/`](protocol) | The contract: [spec](protocol/spec.md), JSON Schema, golden fixtures |

Security notes are in [docs/security.md](docs/security.md); the plan for the
remaining phases is in [docs/roadmap.md](docs/roadmap.md).

## Status

| Phase | Scope | State |
|---|---|---|
| 1 | Protocol spec, agent on macOS/Linux, pairing, CLI client | done |
| 2 | Claude Code hooks, `install-hooks`, Claude session states | done |
| 3 | iOS app: pairing, machine list, terminal, reconnect | built; on-device testing next |
| 4 | Radix + APNs, notifications with reply | server side done; app side waits for phase 3 |
| 5 | Windows (ConPTY, logon task), hardening | done, except items that need the app or a Windows desktop |

## Build

Needs Go 1.26 or newer.

```sh
cd agent
go build -o hedera ./cmd/hedera   # or: go install ./cmd/hedera
go test -race ./...
```

Cross-compile with `GOOS=darwin GOARCH=arm64 go build ./cmd/hedera`, etc.

## Try it

On the machine you want to control, with Tailscale running:

```sh
hedera run                 # foreground; or `hedera install-service` to run at login
hedera new                 # new shell in this terminal; Ctrl-] detaches
hedera ls
hedera attach s_xxxxxxxxxx # reattach; the scrollback is replayed
```

From a second machine on the same tailnet, pair the CLI the same way the app
will pair:

```sh
# machine A
hedera pair                          # shows a QR code and a `hedera remote add …` line
# machine B
hedera remote add 'hedera://pair?…' --name studio
hedera ls --remote studio
hedera new --remote studio -- htop
hedera events --remote studio        # live event stream as JSON lines
```

Type into a session without attaching, the way a notification reply will:

```sh
hedera send s_xxxxxxxxxx "yes, go ahead" --enter
```

Pairing codes work once and expire after five minutes. `hedera tokens` lists
paired devices on A; `hedera tokens revoke <id>` cuts one off immediately.

No Tailscale handy? `hedera run --dev-loopback` also serves the remote (TLS)
API on `127.0.0.1:7373`, so you can pair and connect on a single machine. Use a
separate state directory for the "client" side:

```sh
hedera run --dev-loopback &
hedera pair --no-qr                          # copy the URL
HEDERA_HOME=/tmp/hedera-b hedera remote add '<url>' --name self
HEDERA_HOME=/tmp/hedera-b hedera ls --remote self
```

## Claude Code

```sh
hedera install-hooks --dry-run   # show the change to ~/.claude/settings.json
hedera install-hooks             # merge hedera's hooks in (backs up the file first)
hedera new --claude              # or run `claude` yourself inside any hedera shell
hedera ls                        # STATUS shows starting / working / idle / needs_input / failed
hedera events                    # claude.turn_finished, claude.needs_input, claude.failed, claude.ended
hedera resume                    # Claude sessions lost when the agent stopped; `hedera resume <id>` brings one back
```

`install-hooks` only adds its own entries (a `hedera hook` command on nine
events) and leaves your other hooks, settings and key order alone. Run it again
after moving the binary; `--uninstall` takes the entries out again. It honours
`CLAUDE_CONFIG_DIR`, and `--settings PATH` edits any other settings file.

The hook does nothing outside a hedera session, never prints, and always exits
0, so it can't change how Claude Code behaves. Claude Code holds hooks back in
a folder until you accept its trust dialog, so a new folder shows `starting`
until then. The state machine is described in
[protocol/spec.md §6](protocol/spec.md#6-claude-state).

## Push notifications

Radix delivers notifications to your phone; see [relay/README.md](relay/README.md)
to deploy it and to check that it can reach APNs. The app will configure each
machine's push route when it pairs. Until then, `hedera push configure` sets a
route by hand:

```sh
hedera push configure --radix https://radix.example.workers.dev --token hrp_…
hedera push test
hedera push status       # routes, queue length, last delivery, errors
```

A shell that exits by itself, a Claude turn that finishes, needs input, fails
or ends all notify (see [protocol/spec.md §7.3](protocol/spec.md#73-what-notifies)).
Sessions you close from a client, agent restarts, and exits right after you
typed (`exit`, `/exit`) don't. Notifications are queued on disk and retried if
Radix is unreachable.

## Windows

`hedera` runs on Windows 10 1809 or newer (it needs ConPTY). Sessions default
to PowerShell 7, then Windows PowerShell, then `cmd.exe`.
`hedera install-service` registers a Task Scheduler task that starts the agent
when you log on, in your desktop session, and logs to
`%AppData%\hedera\hedera.log`. `install-hooks` writes exec-form hooks there,
which run without a shell.

## Configuration

`config.json` in the state directory holds the machine ID and optional
settings: `port` (7373), `local_port` (7374), `scrollback_bytes`,
`max_sessions`, `dns_name` (advertised in pairing QR codes), and
`allowed_users` / `allowed_tags` to admit only some tailnet identities (see
[docs/security.md](docs/security.md#tailnet-identity-optional)). Restart the
agent after editing it.

## State

The agent keeps its state in `~/Library/Application Support/hedera` (macOS),
`~/.config/hedera` (Linux) or `%AppData%\hedera` (Windows); `HEDERA_HOME`
overrides it. See [`agent/internal/config`](agent/internal/config/config.go)
for the files.
