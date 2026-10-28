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

### Tailnet identity (optional)

By default any device on your tailnet can reach the listener, though it still
needs a token. Setting `allowed_users` (tailnet logins such as
`you@github`) or `allowed_tags` (such as `tag:servers`) in `config.json`
makes the agent ask `tailscale whois` about every remote peer and refuse
anyone else, pairing requests included. It fails closed: if Tailscale can't
identify a peer, or the `tailscale` CLI isn't installed, connections are
refused (and the agent won't start). Tagged nodes are matched by tag only,
never by their placeholder owner.

## Credentials

| Credential | Where it lives | Grants |
|---|---|---|
| Pairing token (`hdt_…`) | client Keychain / `remotes.json`; agent stores SHA-256 only | full remote API |
| Pairing code | QR code, 5-minute single use | one `POST /v1/pair` |
| Local admin token (`hlt_…`) | `local.token`, mode 0600 | local API: sessions, pairing, revocation |
| Hook token (`hht_…`) | env of one session's processes | posting hook events for that session only |
| Push token (`hrp_…`) | agent `push.json` (0600); Radix stores SHA-256 only | sending notifications to one linked device; no shell access |
| Device secret (`hrd_…`) | the app; Radix stores SHA-256 only | managing that device's registration and links in Radix |

- Every comparison is constant-time (`crypto/subtle`), and the token check
  compares against every stored hash rather than stopping at the first match.
- Tokens are 32 random bytes. They never appear in logs; logs name pairings by
  ID (`p_…`). As a backstop, the logger masks anything shaped like a hedera or
  Radix credential (`hdt_`, `hlt_`, `hht_`, `hrp_`, `hrd_`) in every message
  and value, including inside error strings.
- A paired device can revoke itself (`pairing.unpair`), so deleting a machine
  in the app really cuts it off rather than just forgetting the token.
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

## Push

- The APNs key lives only in Radix, as a Worker secret. Radix never sees a
  pairing token, so a compromised relay can send notifications but can't open
  shells.
- Radix chooses each notification's category and interruption level from its
  `kind`, and takes the machine ID from the link, so a leaked push token can
  neither break through Focus at will nor impersonate another machine.
- The agent accepts only `https` Radix URLs (plain `http` for loopback only)
  and verifies a push token with Radix before storing it.
- Revoking a pairing removes its push route; deleting the link in Radix makes
  the agent stop using the token.
- Notification text (session titles, Claude's last message) passes through
  Radix and APNs in plaintext until the optional end-to-end encryption in
  phase 5.

## TLS

The Tailscale tunnel is encrypted already, but it is not trusted on its own.
The agent uses a self-signed ECDSA P-256 certificate (TLS 1.3 only) and clients
pin `sha256(SubjectPublicKeyInfo)` from the QR code. A wrong pin aborts the
handshake before any credential is sent.

The iOS app connects with Network.framework, not URLSession. URLSession applies
App Transport Security, which re-checks a pinned certificate against the system
trust store and rejects the agent's self-signed one on any address but
loopback. ATS stays on; the app's connections to agents don't go through it, and
the pin in the TLS verify block is the only check.

## State files

The state directory is mode 0700; `local.token`, `pairings.json`,
`remotes.json` and the TLS key are 0600 and written atomically.

## Known gaps

- Restarting the agent ends all sessions, since the agent owns the PTYs.
