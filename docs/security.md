# Security model

Whoever holds a valid credential for an agent can run any command as the user
on that machine, so the agent treats every network path as hostile.

## Network exposure

- The remote listener binds only to Tailscale addresses (`100.64.0.0/10`,
  `fd7a:115c:a1e0::/48`), never `0.0.0.0`. It re-scans interfaces every 10 s
  and binds new Tailscale addresses as they appear.
- It also rejects any peer whose source address is outside those ranges.
- The local API binds `127.0.0.1` only and rejects non-loopback peers.
- `--dev-loopback` adds a TLS listener on `127.0.0.1` for testing; it cannot
  bind anything but loopback.

## Credentials

| Credential | Where it lives | Grants |
|---|---|---|
| Pairing token (`hdt_…`) | client Keychain / `remotes.json`; agent stores SHA-256 only | full remote API |
| Pairing code | QR code, 5-minute single use | one `POST /v1/pair` |
| Local admin token (`hlt_…`) | `local.token`, mode 0600 | local API: sessions, pairing, revocation |
| Hook token (`hht_…`) | env of one session's processes | posting hook events for that session only |
| Push token (Phase 4) | agent | sending notifications via Radix; no shell access |

- Every comparison is constant-time (`crypto/subtle`), and the token check
  compares against every stored hash rather than stopping at the first match.
- Tokens are 32 random bytes. They never appear in logs; logs name pairings by
  ID (`p_…`).
- Failed pairing attempts are limited to 10 per peer address per 10 minutes,
  though a 160-bit code can't be guessed anyway.
- Revoking a pairing deletes its hash and closes its live connections with
  close code 4003.

## Hooks

- `hedera hook` sends the hook token only to a loopback `http://` URL, so a
  tampered `HEDERA_HOOK_URL` can't carry it off the machine.
- `/v1/hook` returns the same 401 for a wrong token and for an unknown
  session, so it doesn't reveal which sessions exist.
- Any process inside a session can report Claude events for that session. That
  is the trust boundary: it already runs as you, in that terminal.
- `install-hooks` refuses to touch a settings file it can't parse, and writes a
  timestamped backup before every change.

## TLS

The Tailscale tunnel is encrypted already, but it is not trusted on its own.
The agent uses a self-signed ECDSA P-256 certificate (TLS 1.3 only) and clients
pin `sha256(SubjectPublicKeyInfo)` from the QR code. A wrong pin aborts the
handshake before any credential is sent.

## State files

The state directory is mode 0700; `local.token`, `pairings.json`,
`remotes.json` and the TLS key are 0600 and written atomically.

## Known gaps

- Peer identity within the tailnet is not checked yet (any device on your
  tailnet can try to connect, but still needs a token). Phase 5 may add
  Tailscale `whois` checks.
- Push payloads will pass through Radix and APNs in plaintext until the
  optional end-to-end encryption in Phase 5.
- Restarting the agent ends all sessions, since the agent owns the PTYs.
