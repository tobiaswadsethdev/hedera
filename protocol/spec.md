# Hedera agent protocol, version 1

This document is the contract between the `hedera` agent and its clients (the
Hedera iOS app and the `hedera` CLI). The JSON Schemas in `schemas/` and the
golden fixtures in `fixtures/` are normative; if this text and a schema
disagree, the schema wins and this text is a bug.

An agent advertises what it implements through `capabilities` in the
handshake.

## 1. Transport

| | Remote (phone, other machines) | Local (same machine) |
|---|---|---|
| Listen address | Tailscale addresses only (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`) | `127.0.0.1` |
| Default port | 7373 | 7374 |
| TLS | yes, self-signed, pinned by SPKI hash | no (loopback) |
| WebSocket URL | `wss://<host>:7373/v1/ws` | `ws://127.0.0.1:7374/v1/ws` |
| Credential | pairing token (from `/v1/pair`) | local admin token (file, mode 0600) |

- The agent never listens on any other interface. It re-checks interface
  addresses periodically and binds Tailscale addresses as they appear.
- The remote listener also rejects any peer whose source address is outside the
  Tailscale ranges.
- The WebSocket subprotocol `hedera.v1` must be offered by the client
  (`Sec-WebSocket-Protocol: hedera.v1`); upgrades without it get HTTP 400.

### 1.1 TLS pinning

On first run the agent generates an ECDSA P-256 key and a self-signed
certificate. Its fingerprint is

```
fp = "sha256:" + base64url_nopad( SHA-256( cert.SubjectPublicKeyInfo ) )
```

Clients must not use the system trust store for agents. They accept the
connection if and only if the leaf certificate's SPKI hash equals the pinned
`fp`. Hostname verification is not performed (the pin is stronger).

### 1.2 Authentication

Every HTTP request and WebSocket upgrade carries

```
Authorization: Bearer <token>
```

Missing or wrong credentials get HTTP 401 with an empty body, before any
upgrade. Tokens are 32 random bytes, base64url without padding, prefixed
`hdt_`. The agent stores only `SHA-256(token)` and compares in constant time.
Tokens never appear in logs.

When a pairing is revoked, the agent closes every WebSocket authenticated with
it using close code `4003`.

### 1.3 Pairing

`hedera pair` asks the running agent for a one-time pairing code and shows a QR
code encoding this URL:

```
hedera://pair?v=1&m=<machine_id>&n=<hostname>&a=<tailscale_ip>[&a=<ip2>][&d=<magicdns_name>]&p=<port>&fp=<fp>&c=<code>
```

| Param | Meaning |
|---|---|
| `v` | pairing URL version, `1` |
| `m` | machine ID |
| `n` | display name (hostname) |
| `a` | Tailscale address; may repeat |
| `d` | MagicDNS name, when known |
| `p` | remote port |
| `fp` | TLS fingerprint (section 1.1) |
| `c` | one-time pairing code: 20 random bytes, base32 lowercase, no padding |

The code is single use and expires after 5 minutes. To pair, the client opens a
TLS connection to `a`/`d` on `p`, checks the pin, and sends:

```
POST /v1/pair
Content-Type: application/json

{"code":"<c>","device_name":"Tobias's iPhone"}
```

Success (200):

```json
{"pairing_id":"p_k3m7x2qa","token":"hdt_…","machine_id":"m_…","name":"studio"}
```

Failure is 401 for an unknown, used or expired code; 400 for a malformed body;
429 when too many failed attempts came from the peer address recently. The code
carries no authority beyond this single request.

## 2. Framing

- **Text frames** carry one JSON object each (UTF-8, at most 1 MiB).
- **Binary frames** carry PTY bytes (at most 64 KiB of payload).

### 2.1 Binary frames

All integers are big-endian.

```
OUTPUT  agent → client   [0x01][channel: u32][offset: u64][payload]
INPUT   client → agent   [0x02][channel: u32][payload]
```

- `channel` is assigned by the agent in the `session.attach` response and is
  scoped to one WebSocket connection.
- `offset` is the absolute position of the first payload byte in the session's
  output stream, counted from 0 at spawn. It never resets for a session.
- An INPUT frame for a channel the connection does not hold is ignored.
- A frame with an unknown opcode or a truncated header closes the connection
  with `4008`.

### 2.2 Control messages

Every control message has a string field `t`.

**Requests** (client → agent) have a client-chosen integer `id`, unique among
the connection's outstanding requests:

```json
{"t":"session.resize","id":7,"session_id":"s_…","cols":120,"rows":40}
```

**Responses** (agent → client) have `t:"res"`, `re` equal to the request `id`,
and `ok`. On success the result fields sit alongside; on failure there is an
`error` object:

```json
{"t":"res","re":7,"ok":true}
{"t":"res","re":7,"ok":false,"error":{"code":"not_found","message":"no such session"}}
```

Responses may arrive in a different order than requests were sent.

**Unsolicited messages** (agent → client) have no `re`: `event`, `lagged`,
`detached`, `events.lagged`.

Unknown fields must be ignored by both sides. Unknown request types get
`error.code = "unknown_type"`.

### 2.3 Error codes

| Code | Meaning |
|---|---|
| `unsupported_version` | no common protocol version |
| `unknown_type` | request `t` not recognised |
| `invalid_argument` | a parameter is missing, malformed or out of range |
| `not_found` | no such session or channel |
| `session_exited` | the session's process has exited |
| `session_running` | the operation needs an exited session |
| `limit_exceeded` | e.g. too many sessions |
| `stale` | an `if_turn` precondition failed |
| `unavailable` | a service the agent depends on (Radix) could not be reached |
| `internal` | anything else; the message is safe to show |

### 2.4 Close codes

| Code | Meaning |
|---|---|
| 1000 | normal |
| 4001 | handshake failed (`unsupported_version`, or no `hello` within 10 s) |
| 4003 | credential revoked |
| 4008 | protocol violation (request before `hello`, bad binary frame, oversized message) |
| 4009 | write timeout (client too slow to read control messages) |

### 2.5 Keepalive

The agent sends WebSocket pings every 20 s and closes the connection if a pong
does not arrive within 10 s. Clients may ping too.

## 3. Handshake

The first message on a connection must be `hello`:

```json
{"t":"hello","id":1,"protocol":{"min":1,"max":1},
 "client":{"name":"Hedera-iOS","version":"0.1.0","platform":"ios"}}
```

The agent picks the highest version in both ranges:

```json
{"t":"res","re":1,"ok":true,"protocol":1,
 "machine_id":"m_…","boot_id":"b_…",
 "agent":{"version":"0.1.0","os":"darwin","arch":"arm64","hostname":"studio"},
 "capabilities":["shell"],
 "limits":{"max_sessions":32,"scrollback_bytes":4194304,"max_frame":65536},
 "server_time":"2026-10-27T21:00:00.000Z"}
```

`boot_id` changes every time the agent process starts. Event sequence numbers
are only comparable within one `boot_id`.

Capabilities defined so far:

| Capability | Meaning |
|---|---|
| `shell` | shell sessions (always present) |
| `claude` | Claude Code hook integration (section 6) |
| `push` | push notifications through Radix (section 7) |

## 4. Sessions

### 4.1 Session object

```json
{
  "id": "s_4fj7q2m3xa",
  "kind": "shell",
  "title": "zsh — hedera",
  "cwd": "/Users/t/src/hedera",
  "argv": ["/bin/zsh", "-l"],
  "pid": 4121,
  "created_at": "2026-10-27T21:00:00.000Z",
  "size": {"cols": 120, "rows": 40},
  "status": "running",
  "output_offset": 918233,
  "attached": 1,
  "claude": null,
  "exit": null
}
```

| Field | Notes |
|---|---|
| `kind` | `shell` or `claude` |
| `argv` | the command as requested, or the default the agent chose (without the login-shell wrapper) |
| `status` | see 4.2 |
| `output_offset` | total bytes of output so far (the next byte's offset) |
| `attached` | number of channels attached across all connections |
| `claude` | Claude state (section 6). Always set for `kind:"claude"`; set for a shell once Claude Code runs inside it; otherwise `null` |
| `exit` | `null` while running; afterwards `{"code":0,"signal":null,"at":"…","initiated_by":"process"}` |

`exit.code` is the exit status, or `null` if the process was killed by a
signal; `exit.signal` is the signal name (`"SIGHUP"`, `"SIGKILL"`, …) or `null`.
`initiated_by` is `"client"` when the exit followed a `session.close`,
`"agent"` when the agent hung up the session because it was shutting down, and
`"process"` otherwise. Only `"process"` exits trigger push notifications.

### 4.2 Status

```
shell:   running ──────────────────────────────────────────► exited
claude:  starting ─► working ⇄ idle | needs_input | failed ─► exited
```

| Status | Meaning |
|---|---|
| `running` | shell process alive |
| `starting` | `claude` spawned, no `SessionStart` hook yet (also: hooks not installed, or the folder's trust dialog not yet accepted) |
| `working` | a prompt was submitted and Claude is working on it |
| `idle` | the turn finished; Claude waits for the next prompt |
| `needs_input` | Claude is blocked on a permission prompt or question |
| `failed` | the last turn ended with an API error; the process is still alive |
| `exited` | the process has exited; output stays readable until removed |

For `kind:"claude"` the session status mirrors `claude.status`. A shell stays
`running` even while Claude Code runs inside it; its `claude` object carries the
Claude status.

Exited sessions are kept (with their scrollback) until `session.remove`, until
they have been exited for 24 hours, or until more than 32 exited sessions exist
(oldest go first).

### 4.3 `session.list`

```json
→ {"t":"session.list","id":2}
← {"t":"res","re":2,"ok":true,"sessions":[ …Session… ]}
```

Sessions are ordered by `created_at`.

### 4.4 `session.create`

```json
→ {"t":"session.create","id":3,"kind":"shell","cwd":"~/src/hedera",
   "argv":null,"env":{"FOO":"bar"},"cols":120,"rows":40,"title":null}
← {"t":"res","re":3,"ok":true,"session":{ …Session… }}
```

| Param | Default | Notes |
|---|---|---|
| `kind` | — (required) | `shell` or `claude` |
| `cwd` | home directory | `~` and `~/…` are expanded; must be an existing directory |
| `argv` | login shell, or `["claude"]` | non-empty array of strings |
| `env` | `{}` | extra variables; keys starting with `HEDERA_` are rejected |
| `cols`, `rows` | 80, 24 | 1–1000 |
| `title` | derived from command and cwd | at most 200 characters |

On Unix the agent runs commands through the user's login shell
(`$SHELL -l -c 'exec "$0" "$@"' argv…`) so that `PATH` matches an interactive
login, even when the agent runs under launchd or systemd. Every process gets:

| Variable | Value |
|---|---|
| `HEDERA_PTY_ID` | the session ID |
| `HEDERA_MACHINE_ID` | the machine ID |
| `HEDERA_HOOK_TOKEN` | a random secret for this session only, used by hooks |
| `HEDERA_HOOK_URL` | where `hedera hook` posts hook events (`http://127.0.0.1:7374/v1/hook`) |
| `TERM` | `xterm-256color` |
| `COLORTERM` | `truecolor` |

Inherited copies of these variables are replaced, and Claude Code's
"running inside Claude Code" markers (`CLAUDECODE`, `CLAUDE_CODE_CHILD_SESSION`,
`CLAUDE_CODE_SESSION_ID`, `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_PID`) are removed,
so a session looks like a fresh terminal even if the agent was started from one.

The new session is also announced as a `session.created` event.

### 4.5 `session.attach`

```json
→ {"t":"session.attach","id":4,"session_id":"s_…","from_offset":0,"cols":120,"rows":40}
← {"t":"res","re":4,"ok":true,"channel":1,"session":{ …Session… },
   "replay":{"from":0,"to":918233,"truncated":false}}
```

After the response, the agent sends OUTPUT frames on `channel`: first the
replay (bytes `replay.from` up to `replay.to`), then live output starting at
`replay.to`. Registration is atomic, so there are no gaps or duplicates between
replay and live data.

- `from_offset` omitted: replay everything still in the scrollback buffer.
- `from_offset` inside the buffer: replay from there. A reconnecting client
  passes the offset after the last byte it rendered, and continues seamlessly.
- `from_offset` older than the buffer, or past the end: replay starts at the
  oldest buffered byte, moved forward to a UTF-8 character boundary, and
  `truncated` is `true`. The client should reset its terminal (for example by
  writing `ESC c`) before rendering the replay.
- `cols`/`rows` given: the session is resized first (see 4.7).

Attaching an exited session is allowed: the client gets the replay followed by
a `detached` message.

A connection may hold several channels, including several on one session.

### 4.6 Input

Interactive input is sent as binary INPUT frames on an attached channel.

`session.input` is the JSON form; it needs no attach, which is what a
notification reply uses:

```json
→ {"t":"session.input","id":5,"session_id":"s_…","data":"yes, go ahead","submit":true,"if_turn":7}
← {"t":"res","re":5,"ok":true}
```

- `data`: UTF-8 text, at most 64 KiB.
- `submit: true`: after writing `data` the agent waits about 50 ms and writes a
  carriage return (`\r`) separately, so full-screen TUIs treat it as Enter
  rather than part of a paste.
- `if_turn`: reject with `stale` unless the session's `claude.turn` still
  equals this value (`invalid_argument` if the session has no Claude state).
  A notification reply passes the `turn` from the event it answers.

Input from different connections to one session is serialized; a `submit`
sequence is never interleaved with other input.

### 4.7 `session.resize`

```json
→ {"t":"session.resize","id":6,"session_id":"s_…","cols":120,"rows":40}
← {"t":"res","re":6,"ok":true}
```

The PTY has one size. The most recent resize wins, whichever client sent it.
Every resize is announced as a `session.resized` event.

### 4.8 `session.detach`

```json
→ {"t":"session.detach","id":7,"channel":1}
← {"t":"res","re":7,"ok":true}
```

No more OUTPUT frames are sent on that channel after the response.

### 4.9 `session.close`

```json
→ {"t":"session.close","id":8,"session_id":"s_…","mode":"hangup"}
← {"t":"res","re":8,"ok":true}
```

- `hangup` (default): send `SIGHUP` to the session's process group, then
  `SIGKILL` if it is still alive after 5 seconds.
- `kill`: `SIGKILL` the process group immediately.

The response means the signal was sent, not that the process has exited; the
`session.exited` event reports that.

### 4.10 `session.remove`

```json
→ {"t":"session.remove","id":9,"session_id":"s_…"}
← {"t":"res","re":9,"ok":true}
```

Fails with `session_running` unless the session has exited. Frees its
scrollback.

### 4.11 Channel messages

**`lagged`**: the client did not read live output fast enough (more than about
1 MiB queued for the channel). The agent stops the channel. The client
re-attaches with `from_offset` set to `offset`, which is the first byte it did
not receive:

```json
{"t":"lagged","channel":1,"session_id":"s_…","offset":918233}
```

**`detached`**: a channel ended without a `session.detach`. All of the
session's output has been sent first.

```json
{"t":"detached","channel":1,"session_id":"s_…","reason":"exited"}
```

`reason` is `exited` or `removed`.

## 5. Events

### 5.1 `events.subscribe`

```json
→ {"t":"events.subscribe","id":10,"since_seq":41}
← {"t":"res","re":10,"ok":true,"seq":44,"reset":false}
```

`seq` is the agent's latest event sequence number. If `since_seq` is given and
the agent still has every event after it, those events (42, 43, 44 here) follow
the response, then live events. Otherwise `reset` is `true`, no backlog is
sent, and the client should call `session.list` to rebuild its state.

Without `since_seq`, no backlog is sent and `reset` is `false`.

Subscribing again on the same connection replaces the subscription.
`events.unsubscribe` ends it.

If the client falls behind (more than 256 queued events) the agent ends the
subscription with

```json
{"t":"events.lagged","seq":44}
```

where `seq` is the last event delivered; the client subscribes again with
`since_seq` set to it.

### 5.2 Event envelope

```json
{"t":"event","seq":42,"ts":"2026-10-27T21:00:00.000Z","type":"session.created", …}
```

`seq` increases by one per event, starting at 1 for each `boot_id`.

| Type | Fields |
|---|---|
| `session.created` | `session` |
| `session.updated` | `session`, `reason` (`attached`, `detached`, `claude`) |
| `session.resized` | `session_id`, `cols`, `rows` |
| `session.exited` | `session` (with `exit` set) |
| `session.removed` | `session_id` |
| `claude.turn_finished` | `session`, `turn`, `preview` (may be absent) |
| `claude.needs_input` | `session`, `turn`, `kind`, `message` |
| `claude.failed` | `session`, `turn`, `error`, `error_details` (may be absent) |
| `claude.ended` | `session`, `turn`, `reason` |

Every event that carries `session` holds a full snapshot taken when the event
happened; clients replace their copy with it. A Claude state change is
published either as one `claude.*` event or, when it is not notification
worthy, as `session.updated` with reason `claude`, never both.

## 6. Claude state

Claude Code reports its state through hooks. `hedera install-hooks` registers
`hedera hook` for the events below; the hook forwards the payload to
`/v1/hook` (section 8) with the session's `HEDERA_HOOK_TOKEN`.

```json
{"status":"needs_input","session_id":"abc123","turn":7,
 "needs_input":{"kind":"permission_prompt","message":"Claude needs your permission to use Bash"},
 "last_message_preview":"I've updated the tests…",
 "last_error":null,
 "end_reason":null}
```

| Field | Meaning |
|---|---|
| `status` | `starting`, `working`, `idle`, `needs_input`, `failed`, or `ended` (Claude Code exited; the process it ran in may live on) |
| `session_id` | Claude Code's own session ID, once known |
| `turn` | increases by one on every prompt; never resets for a PTY, so a stale `if_turn` can never match again |
| `needs_input` | what Claude is waiting for, while `status` is `needs_input` |
| `last_message_preview` | the last turn's final message, whitespace collapsed, at most 280 characters |
| `last_error` | `{error, error_details}` from the last failed turn, cleared by the next prompt |
| `end_reason` | from `SessionEnd` (`prompt_input_exit`, `logout`, `other`), or `exited` if Claude Code ended without one |

### 6.1 Transitions

| Hook | Effect | Event |
|---|---|---|
| `SessionStart` | record `session_id`; `idle` (except `source:"compact"`, which changes nothing) | `session.updated` |
| `UserPromptSubmit` | `turn += 1`; `working`; clear `needs_input` and `last_error` | `session.updated` |
| `PermissionRequest` | `needs_input` (`kind:"permission_prompt"`) | `session.updated` only |
| `Notification` `permission_prompt`, `elicitation_dialog`, `elicitation_url_dialog`, `agent_needs_input` | `needs_input` | `claude.needs_input`, once per waiting episode |
| `Notification` `elicitation_complete`, `elicitation_response` | `needs_input` → `working` | `session.updated` |
| `Notification` `idle_prompt` | `idle`, only if no `Stop` was seen for this turn | `claude.turn_finished` in that case |
| `PostToolUse`, `PostToolUseFailure` | `needs_input` → `working` (the prompt was answered) | `session.updated` |
| `Stop` | `idle`; update `last_message_preview` | `claude.turn_finished` |
| `StopFailure` | `failed`; set `last_error` | `claude.failed` |
| `SessionEnd` `clear`, `resume` | nothing yet; the next `SessionStart` rebinds | — |
| `SessionEnd` other reasons | `ended`; set `end_reason` | shell sessions: `claude.ended`; claude sessions: see below |
| process exit | `ended` (`end_reason:"exited"` if not set) | claude sessions: `claude.ended`, right after `session.exited` |

Notes:

- `PermissionRequest` fires as soon as Claude asks, so the status is accurate
  at once. Claude Code sends the `permission_prompt` notification only after
  the prompt has waited about six seconds, so someone at the keyboard who
  answers quickly causes no push.
- For `kind:"claude"`, `claude.ended` waits for the process to exit, so there
  is a single notification per session.
- Hooks carrying a different Claude `session_id` than the one bound to the PTY
  are ignored (e.g. a `claude -p` run by a tool inside the session), except a
  `SessionStart` after `SessionEnd` `clear`/`resume`, after `ended`, or with
  `source` `clear`, `resume` or `fork`, which rebinds the PTY to the new ID.
- Hooks after the process exited, and unknown hook events, are ignored.

## 7. Push

Agents send notifications through Radix, the push relay; its API is in
[radix-api.md](radix-api.md). The app registers with Radix, links the machine,
and hands the agent the link's push token over this connection. Push settings
belong to the connection's credential: each paired device has its own route,
and revoking the pairing removes it.

### 7.1 `push.configure`

```json
→ {"t":"push.configure","id":11,"radix_url":"https://radix.example.workers.dev",
   "push_token":"hrp_…","prefs":{"claude_turn_finished":false}}
← {"t":"res","re":11,"ok":true,"configured":true,"radix_url":"https://radix.example.workers.dev",
   "label":"studio","prefs":{"shell_exited":true,"claude_turn_finished":false,"claude_needs_input":true,
   "claude_failed":true,"claude_ended":true},"queued":0}
```

- `radix_url` must be `https` (plain `http` only for loopback, in development).
- The agent checks the token with Radix (`GET /v1/link`) before storing it:
  a rejected token is `invalid_argument`, an unreachable Radix `unavailable`.
- `prefs` switches kinds off; missing kinds are on. Keys are `shell_exited`,
  `claude_turn_finished`, `claude_needs_input`, `claude_failed`,
  `claude_ended`.
- Configuring again replaces the route and drops anything still queued for
  the old one.

### 7.2 `push.status` and `push.remove`

```json
→ {"t":"push.status","id":12}
← {"t":"res","re":12,"ok":true,"configured":true,"radix_url":"…","label":"studio","prefs":{…},
   "last_success_at":"2026-10-27T21:00:00.000Z","last_error":"","queued":0}
→ {"t":"push.remove","id":13}
← {"t":"res","re":13,"ok":true}
```

`push.status` reports `configured: false` when this credential has no route.
`broken` is set when Radix rejected the token later on; configure again to
repair it.

### 7.3 What notifies

| Event | Kind | Condition |
|---|---|---|
| `session.exited` of a shell | `shell_exited` | `exit.initiated_by` is `process` |
| `claude.turn_finished` | `claude_turn_finished` | always |
| `claude.needs_input` | `claude_needs_input` | always |
| `claude.failed` | `claude_failed` | always |
| `claude.ended` | `claude_ended`, or `claude_ended_abnormally` for a non-zero exit or a signal | the exit was not initiated by a client or the agent |

Exits are not pushed when the session received input in the previous 10
seconds: someone just typed `exit` or `/exit` and knows. The notification's
title is the session title, the subtitle the machine's hostname, and its
`event_id` is `<boot_id>-<seq>`, so retries are recognised as duplicates.

Notifications are queued on disk and retried with backoff (honouring
`Retry-After`) for up to six hours, matching the APNs expiration Radix sets.

## 8. Local-only HTTP API

These routes exist only on the `127.0.0.1` listener and require the local admin
token, except `/v1/hook`. The `hedera` CLI uses them; the app never does.

| Route | Purpose |
|---|---|
| `GET /v1/ws` | same WebSocket protocol as remote |
| `POST /v1/local/pair-codes` | mint a pairing code; returns `{code_id, url, expires_at}` |
| `GET /v1/local/pair-codes/{code_id}/wait` | long-poll (≤ 30 s): `{status:"pending"|"completed"|"expired", pairing_id?, device_name?}` |
| `GET /v1/local/pairings` | `{pairings:[{id, device_name, created_at, last_seen_at}]}` |
| `DELETE /v1/local/pairings/{id}` | revoke; closes that pairing's connections with `4003` |
| `POST /v1/hook` | Claude Code hook events (see below) |
| `GET /v1/local/push` | every push route with its status (no tokens) |
| `POST /v1/local/push/test` | queue a `test` notification for every route; returns `{queued}` |

`POST /v1/hook` takes the hook's JSON payload unchanged as the body (at most
1 MiB), with `Authorization: Bearer <HEDERA_HOOK_TOKEN>` and
`X-Hedera-Session: <HEDERA_PTY_ID>`. A token that is not that session's gets
401, whether or not the session exists. Success is 204.

## 9. Versioning rules

- Adding a message type, field, event type, error code or capability does not
  change the protocol version. Receivers ignore what they don't know.
- Removing or changing the meaning of anything increments the version. Agents
  keep supporting older versions in `hello` for at least one app release.
