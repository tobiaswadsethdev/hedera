# Radix API, version 1

Radix is the push relay: a Cloudflare Worker that holds the APNs key and is
the only thing that talks to Apple. The Hedera app registers its device with
Radix and links machines to it; each agent then sends notification events to
Radix with a push-only token.

```
app ──register device, link machine──► Radix ◄──events (push token)── hedera agent
                                         │
                                         └──► APNs ──► iPhone
```

## Principles

- **Agents never hold the APNs key**, and Radix never holds an agent's pairing
  token. A push token can make your phone show a notification and nothing
  more.
- **Radix decides how loud a notification is.** The agent sends a `kind`;
  Radix maps it to an APNs category and interruption level, so a leaked push
  token can't send arbitrary payloads or break through Focus at will.
- **Radix stores hashes of secrets only** (SHA-256), and never logs them.
- **The machine ID in a notification comes from the link**, not from the
  agent's request, so one machine can't impersonate another.

## Conventions

- JSON bodies, `Content-Type: application/json`, at most 16 KiB.
- `Authorization: Bearer <secret>` where a route needs a credential.
- Errors: `{"error": {"code": "…", "message": "…"}}` with the HTTP status
  below. The codes are `bad_request`, `unauthorized`, `not_found`,
  `rate_limited`, `apns_error` and `internal`.
- Rate-limited responses carry `Retry-After` (seconds).

| Credential | Format | Issued by | Used for |
|---|---|---|---|
| Device secret | `hrd_` + 43 base64url chars | `POST /v1/devices` | the app managing its device and links |
| Push token | `hrp_` + 43 base64url chars | `POST /v1/machines` | one agent sending events for one link |

IDs: devices `d_` + 16 base32 characters, links `l_` + 16.

## Device routes (used by the app)

### `POST /v1/devices`

Registers an iPhone. No credential; limited to 10 registrations per source
address per hour.

```json
→ {"apns_token": "<hex device token>", "environment": "production"}
← 201 {"device_id": "d_…", "device_secret": "hrd_…"}
```

`environment` is `sandbox` for development builds and `production` for
TestFlight and App Store builds. `apns_token` is 64–200 hex characters.

### `PUT /v1/devices/{device_id}`

The app calls this whenever iOS hands it a new device token. It also re-enables
a device that APNs had reported as unregistered.

```json
→ {"apns_token": "…", "environment": "production"}
← 200 {"device_id": "d_…"}
```

### `DELETE /v1/devices/{device_id}`

Removes the device and every link to it. `204`.

### `POST /v1/devices/{device_id}/test`

Sends a test notification straight to the device, and reports what APNs said:

```json
← 200 {"sent": true, "apns_status": 200, "apns_id": "…"}
← 200 {"sent": false, "apns_status": 400, "apns_reason": "BadDeviceToken"}
```

This doubles as the connectivity check in the roadmap: register a device
with any well-formed token and call this. An answer with `apns_status` 400 and
`BadDeviceToken` proves that Radix reached APNs over HTTP/2 and that its JWT
was accepted.

## Link routes (used by the app)

A link connects one device to one agent machine and owns that machine's push
token.

### `POST /v1/machines`

```json
→ {"machine_id": "m_…", "label": "studio"}
← 201 {"link_id": "l_…", "push_token": "hrp_…"}
```

The app then sends the push token to that agent with `push.configure`. A
device can have at most 50 links.

### `GET /v1/machines`

```json
← 200 {"links": [{"link_id": "l_…", "machine_id": "m_…", "label": "studio",
                  "created_at": "2026-10-27T21:00:00.000Z", "last_event_at": null}]}
```

### `DELETE /v1/machines/{link_id}`

Deletes the link; its push token stops working at once. `204`.

## Agent routes

### `GET /v1/link`

Checks a push token. `push.configure` calls this before accepting a token.

```json
← 200 {"link_id": "l_…", "label": "studio", "device_enabled": true}
```

### `POST /v1/events`

Sends one notification to the linked device. Limited to 30 events per link
per minute.

```json
→ {
    "event_id": "b_4pgsopgumy-42",
    "kind": "claude_needs_input",
    "title": "fix flaky test",
    "subtitle": "studio",
    "body": "Claude needs your permission to use Bash",
    "session_id": "s_4fj7q2m3xa",
    "turn": 7
  }
← 200 {"sent": true}
← 200 {"sent": false, "reason": "duplicate"}
← 200 {"sent": false, "reason": "device_unregistered"}
```

| Field | Notes |
|---|---|
| `event_id` | unique per agent; a repeat within 24 hours is acknowledged as `duplicate` and not sent again, so agents can retry safely |
| `kind` | see the table below; anything else is `400` |
| `title` | at most 100 characters (longer is cut) |
| `subtitle` | optional, at most 100 characters |
| `body` | at most 500 characters |
| `session_id`, `turn` | passed through to the app for opening the session and replying; `turn` is optional |

| `kind` | Category | Interruption level |
|---|---|---|
| `shell_exited` | `SHELL_EXITED` | `time-sensitive` |
| `claude_turn_finished` | `CLAUDE_TURN` | `active` |
| `claude_needs_input` | `CLAUDE_INPUT` | `active` |
| `claude_failed` | `CLAUDE_FAILED` | `time-sensitive` |
| `claude_ended` | `CLAUDE_ENDED` | `active` |
| `claude_ended_abnormally` | `CLAUDE_ENDED` | `time-sensitive` |
| `test` | `TEST` | `active` |

How the agent should treat responses:

| Response | Meaning | Agent action |
|---|---|---|
| `200` | handled (sent, duplicate, or the device is gone) | done |
| `400` | malformed event | drop it |
| `401` | push token revoked or unknown | stop using the route |
| `429` | rate limited | retry after `Retry-After` |
| `502` | APNs is unavailable or overloaded | retry with backoff |
| other `5xx`, network errors | Radix trouble | retry with backoff |

When APNs reports the device token as `Unregistered` or `BadDeviceToken`,
Radix disables the device until the app updates it with `PUT`, and answers
events with `device_unregistered`.

## The APNs payload

```json
{
  "aps": {
    "alert": {"title": "fix flaky test", "subtitle": "studio", "body": "Claude needs your permission to use Bash"},
    "sound": "default",
    "category": "CLAUDE_INPUT",
    "thread-id": "m_…:s_4fj7q2m3xa",
    "interruption-level": "active"
  },
  "hedera": {"kind": "claude_needs_input", "machine_id": "m_…", "link_id": "l_…", "session_id": "s_4fj7q2m3xa", "turn": 7}
}
```

Headers: `apns-push-type: alert`, `apns-priority: 10`, `apns-topic` is the
app's bundle ID, and `apns-expiration` is six hours ahead. The JWT is ES256
with the key ID and team ID from Radix's configuration, and is reused for 50
minutes.

## `GET /v1/health`

`200 {"ok": true, "version": "…"}`. No credential.
