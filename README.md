# Hedera

Control terminals and Claude Code sessions on your computers from an iPhone.

| Part | Path | What it is |
|---|---|---|
| `hedera` | [`agent/`](agent) | Go agent on each computer: owns PTYs, serves the WebSocket API over Tailscale, and is also the CLI |
| Radix | `relay/` (Phase 4) | Cloudflare Worker that holds the APNs key and delivers push notifications |
| Hedera app | `ios/` (Phase 3) | SwiftUI app with SwiftTerm terminals |
| Protocol | [`protocol/`](protocol) | The contract: [spec](protocol/spec.md), JSON Schema, golden fixtures |

Security notes are in [docs/security.md](docs/security.md).

## Status

| Phase | Scope | State |
|---|---|---|
| 1 | Protocol spec, agent on macOS/Linux, pairing, CLI client | done |
| 2 | Claude Code hooks, `install-hooks`, Claude session states | next |
| 3 | iOS app: pairing, machine list, terminal, reconnect | |
| 4 | Radix + APNs, notifications with reply | |
| 5 | Windows (ConPTY, logon task), hardening | |

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

## State

The agent keeps its state in `~/Library/Application Support/hedera` (macOS),
`~/.config/hedera` (Linux) or `%AppData%\hedera` (Windows); `HEDERA_HOME`
overrides it. See [`agent/internal/config`](agent/internal/config/config.go)
for the files.
