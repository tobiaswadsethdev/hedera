# Roadmap

What is left to build, in order, with the decisions already made. Phases 1
and 2 are done; see the [README](../README.md) for what they cover and the
[protocol spec](../protocol/spec.md) for how the pieces talk.

| Phase | Scope | State |
|---|---|---|
| 1 | Protocol v1, agent on macOS/Linux, pairing, CLI client | done |
| 2 | Claude Code hooks, `install-hooks`, Claude session states and events | done |
| 3 | Hedera iOS app: pairing, machine list, terminal, reconnect | built; on-device testing next |
| 4 | Radix push relay, notifications with reply | server side done; see below |
| 5 | Windows, then hardening | done, except the items marked below |

The iOS app comes before Radix (the original brief had them the other way
round): push can't be tested end to end without the app, which supplies the
APNs device token, while the app's core needs only the agent.

## Phase 3: Hedera iOS app

**Status.** Built and tested in the simulator against `hedera run
--dev-loopback`: pairing from a `hedera://pair` link, live session status,
shells typed into with the accessory row, and backgrounding and returning with
nothing lost or duplicated. HederaKit's tests cover the reconnect rules
against a fake agent, plus an opt-in run against a real one. Not yet tried:
a real iPhone over Tailscale, and QR scanning with a real camera. See
[ios/README.md](../ios/README.md).

### Scope

- **Pairing**: scan the `hedera://pair` QR code from `hedera pair`, connect
  with the certificate pinned to `fp`, `POST /v1/pair`, and store the token,
  fingerprint and addresses in the Keychain. One entry per machine; removing a
  machine deletes its Keychain item.
- **Face ID lock** whenever the app opens or returns from the background.
- **Machine list**: one WebSocket per machine (Network.framework,
  subprotocol `hedera.v1`, bearer token, pinning in the TLS verify block).
  Shows online/offline and a summary of each machine's sessions.
- **Machine detail**: shells and Claude sessions with live status from
  `events.subscribe`. Create a shell or Claude session (cwd, optional command),
  close, remove.
- **Session view**: SwiftTerm, attached through `session.attach`, with an
  accessory row (Ctrl, Esc, Tab, arrows, `|`, `~`). Resize follows the view,
  and replay plus the `truncated` flag drives a terminal reset.
- **Reconnect**: iOS drops sockets in the background, so reconnecting is core
  rather than hardening. Back off, then re-attach from the last rendered
  offset, re-subscribe with `since_seq`, and rebuild from `session.list` on
  `reset` or a new `boot_id`. Handle `lagged` by re-attaching from its offset.
- **App icon**: an ivy leaf.

### Layout

```
ios/
  project.yml              XcodeGen project (no .pbxproj in git)
  Hedera/                  SwiftUI app
  HederaKit/               Swift package: protocol types and client,
                           frame codec, Keychain, reconnect logic
  HederaNotificationService/  added in phase 4 if push bodies are encrypted
```

HederaKit's tests decode `protocol/fixtures/` exactly as the Go tests do, so
the Swift and Go sides can't drift apart. CI gets a macOS job that builds the
app for the simulator and runs the HederaKit tests. On-device testing is
manual.

### Decisions

- Bundle ID `com.sprigbyte.hedera`, team `5CLNYT78JN` (`ios/Config/Hedera.xcconfig`).
- Minimum iOS version still to decide; iOS 17 proposed and used for now.

### Done when

You can pair a machine by QR, see its sessions update live, open a shell or
Claude session, type into it with the accessory row, lock and reopen the app,
and come back to the same session with nothing lost or duplicated.

## Phase 4: Radix and notifications

**Status.** Radix (`relay/`) and the agent's push support are built and
tested, including an end-to-end run of agent → Radix on workerd → a fake
APNs. Left for later:

- Deploying Radix, then the Workers-to-APNs check below (needs your
  Cloudflare account and `.p8` key; `relay/README.md` has the steps).
- On the phone, built but not yet tried end to end (it needs Radix deployed):
  registering with Radix, linking machines and sending `push.configure`,
  per-machine and per-kind settings, notification categories, opening a
  session from a notification, and the lock-screen reply. Delivery via
  `simctl push`, tapping, and hiding the on-screen session's notifications
  were checked in the simulator.

### First: check that Workers can talk to APNs

APNs requires HTTP/2. Before building Radix, confirm that a Cloudflare Worker
can reach `api.sandbox.push.apple.com` and deliver a push. If it can't, the
fallback is a tiny Go relay (for example on Fly.io) with the same API.

### Radix (Cloudflare Worker, TypeScript, D1)

The APNs `.p8` key lives only in Radix (as a Worker secret). Agents never hold
it. The JWT is signed with WebCrypto (ES256) and reused for about 50 minutes.

| Route | Auth | Purpose |
|---|---|---|
| `POST /v1/devices` | none, rate limited | register an APNs device token (and sandbox or production); returns `device_id` + `device_secret` |
| `PUT /v1/devices/:id` | device secret | the APNs token changed |
| `POST /v1/machines` | device secret | link a machine; returns a `push_token` the app hands to the agent with `push.configure` |
| `DELETE /v1/machines/:id` | device secret | unlink; the push token stops working |
| `POST /v1/events` | push token | send a notification; `event_id` makes retries idempotent |

- **A separate push-only token.** The agent's pairing token opens shells, so
  Radix never sees it. A leaked push token can send notifications to your
  phone and nothing else.
- **Radix picks the category and interruption level** from the event `kind`,
  so a leaked push token can't send arbitrary payloads. The agent only
  supplies the title and body.
- Secrets are stored hashed. Requests are rate limited per device and per
  machine.

The full contract goes in `protocol/radix-api.md`.

### Agent

- `push.configure` stores one push route per pairing, so revoking a pairing
  also stops its pushes. Advertise the `push` capability.
- A durable retry queue on disk, so notifications survive Radix being
  unreachable for a while.
- Push only for exits with `initiated_by: "process"`: no burst of "shell
  exited" when you close sessions or the agent restarts.
- Optionally suppress pushes while a local `hedera attach` is active and
  recently typed in.

### Notifications

| Trigger | Category | Interruption level | Action |
|---|---|---|---|
| Shell exited (not closed by you) | `SHELL_EXITED` | time-sensitive | open |
| `claude.turn_finished` | `CLAUDE_TURN` | active | text reply |
| `claude.needs_input` | `CLAUDE_INPUT` | active | text reply |
| `claude.failed` | `CLAUDE_FAILED` | time-sensitive | open |
| `claude.ended` | `CLAUDE_ENDED` | active, time-sensitive if exit ≠ 0 | open |

- Needs the Time Sensitive Notifications capability on the app ID.
- `thread-id` is the session ID, so one session's notifications group
  together.
- The app suppresses notifications for the session currently on screen.

### Reply from the lock screen

1. The `UNTextInputNotificationAction` wakes the app in the background with
   the text.
2. The app connects to the machine and sends `session.input` with
   `submit: true` and `if_turn` set to the notification's `turn`.
3. On `stale` (you already answered at your desk) or any failure, it posts a
   local "not sent, tap to open" notification.

This only works while the iPhone's Tailscale VPN is connected; on-demand
mode is recommended. Background execution time is short, so the connect, send
and confirm path has to be fast.

### Later, not in phase 4

- Quick Allow/Deny buttons for permission prompts. Mapping them to keystrokes
  is fragile across Claude Code versions. A `PermissionRequest` hook that waits
  for the phone's decision would be the robust version.

## Phase 5: Windows, then hardening

### Windows

**Status.** Built and tested on GitHub's Windows runner: ConPTY sessions with
job objects, `install-service` as a logon task, exec-form hooks, and a CLI
smoke test against a running agent. Not yet tried on a real desktop: the logon
task starting at login (and whether a console window flashes), and
interactive `hedera attach` in a console window.

- **ConPTY** via go-pty is already compiled; it needs real work and tests on a
  Windows runner.
  - No SIGHUP: close by closing the pseudo console, then kill the process
    tree through a job object.
  - Check resize behaviour and UTF-8 code page handling.
- **Default shell**: PowerShell 7, then Windows PowerShell, then `cmd`. No
  login-shell PATH wrapper.
- **`install-service`**: a scheduled task that runs at logon (`schtasks`), not
  a Windows service, because it must spawn shells in your desktop session.
- **Hooks**: register in exec form (`command` + `args`), since hook commands
  may run under PowerShell or Git Bash.
- File permissions for state files: rely on the per-user profile ACLs and
  verify them.

### Hardening

- ~~Structured logs with redaction~~ (done: `--log-format json`, credential
  masking). Log rotation is left to launchd/journald.
- ~~Check peer identity inside the tailnet~~ (done: `allowed_users` /
  `allowed_tags` in `config.json`, checked with `tailscale whois`).
- Optional end-to-end encrypted push bodies, so Radix and Apple see only
  ciphertext (spec section 7.4). The phone generates a key per push link and
  sends it with `push.configure` rather than through the pairing QR: that
  channel is already pinned and encrypted, and every reconfigure rotates the
  key. Agent and Radix halves done; the app's Notification Service Extension
  decrypts.
- ~~Offer `claude --resume` for Claude sessions lost to an agent restart~~
  (done: `session.resumable` / `session.resume`, `hedera resume`).
- ~~Agent upgrades that refuse to restart while sessions are alive~~ (done
  on macOS and Linux: releases built by the tag-triggered release workflow, installed by
  the agent or `hedera upgrade`, restart deferred until no sessions run).
  Still to do: signed releases, Windows, and showing "update pending" in the
  app.
- ~~Revoke a machine from the app~~ (done: `pairing.unpair`).

### Claude chat view

A native view of Claude sessions next to the terminal, built from Claude
Code's JSONL transcript (protocol/spec.md section 6.2): the agent normalizes
the records and streams them, the app shows them as a chat with a composer
that types into the PTY, a Stop button that sends Esc, and a toggle back to
the terminal. Built and tried in the simulator against a real Claude session.
Later: answering permission prompts and questions from the chat through a
`PermissionRequest` hook, and nested subagent transcripts.

## Known risks

- **Restarting the agent ends all sessions.** The agent owns the PTYs (no
  tmux), so upgrades need care. Claude sessions can be resumed afterwards;
  plain shells can't.
- **Replies need Tailscale on the phone.** Without the VPN, a reply fails and
  falls back to "tap to open".
- **Services start with a bare environment.** Shells and `claude` run through
  your shell as an interactive login shell, so they see the PATH your
  terminal does; an rc file that `exec`s another program (tmux, say) when
  interactive takes over those sessions too. Linux needs `loginctl enable-linger` to run while
  you're logged out. macOS may show privacy prompts attributed to `hedera`.
- **Claude Code holds hooks back until a folder is trusted**, so new folders
  show `starting` until you accept the trust dialog.
- **APNs from Workers** is unverified until the phase 4 check.
- **Hook payload drift.** Claude Code's hook events may change. The handled
  set is small, unknown events are ignored, and the shapes are documented in
  `agent/internal/session/claude.go`.

## Decisions log

| Decision | Why |
|---|---|
| Go agent, PTYs via go-pty, no tmux | one static binary, Unix PTY and ConPTY behind one API |
| Tailscale-only listener plus a per-machine bearer token | the tunnel alone is not trusted |
| Self-signed certificate pinned by SPKI hash from the QR | no CA or MagicDNS HTTPS needed; a wrong pin fails before any credential is sent |
| One-time pairing code in the QR, not the token | a photo of the QR is useless after 5 minutes or one use |
| Separate push-only token for Radix | a compromised relay can't open shells |
| Command hook (`hedera hook`) rather than an HTTP hook | reads env directly, does nothing outside hedera, no header-interpolation settings |
| Shell form for hook commands on Unix | works on every Claude Code version; exec form planned for Windows |
| `needs_input` notifications are not time-sensitive | only failures and unexpected shell exits break through Focus |
| iOS app before Radix | push needs the app; the app doesn't need push |
| XcodeGen for the iOS project | no `.pbxproj` merge conflicts |
