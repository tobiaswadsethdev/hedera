# Radix

The Hedera push relay: a Cloudflare Worker with a D1 database. It holds the
APNs key, accepts notification events from `hedera` agents, and is the only
thing that talks to Apple. The API is in
[protocol/radix-api.md](../protocol/radix-api.md).

## Develop

```sh
npm ci            # npm 10.9 has a resolver bug with this tree; use npx npm@11 ci if it errors
npm test          # vitest inside workerd, with D1 and a stubbed APNs
npm run typecheck
```

## Deploy

You need a Cloudflare account and, from the Apple Developer portal, an APNs
key (`AuthKey_XXXXXXXXXX.p8`), its key ID and your team ID.

```sh
npx wrangler login
npx wrangler d1 create radix                     # copy database_id into wrangler.jsonc
# in wrangler.jsonc, set APNS_KEY_ID, APNS_TEAM_ID and APNS_TOPIC (the app's bundle ID)
npx wrangler secret put APNS_KEY_P8 < AuthKey_XXXXXXXXXX.p8
npm run deploy                                   # applies migrations, then deploys
```

## Check that Radix can reach APNs

This is the first step of phase 4 in the [roadmap](../docs/roadmap.md), and it
needs no app. Register a device with a made-up token and ask Radix for a test
push:

```sh
RADIX=https://radix.<your-subdomain>.workers.dev
curl -s $RADIX/v1/devices -H 'content-type: application/json' \
  -d "{\"apns_token\":\"$(printf '0%.0s' {1..64})\",\"environment\":\"sandbox\"}"
# → {"device_id":"d_…","device_secret":"hrd_…"}
curl -s -X POST $RADIX/v1/devices/<device_id>/test -H "authorization: Bearer <device_secret>"
```

| Answer | Meaning |
|---|---|
| `"apns_status":400,"apns_reason":"BadDeviceToken"` | Radix reached APNs over HTTP/2 and the key works; only the fake token was refused |
| `"apns_status":403,"apns_reason":"InvalidProviderToken"` | reached APNs, but the key ID, team ID or `.p8` is wrong |
| `"apns_status":0` | Radix never got an answer; Workers couldn't reach APNs |

Delete the test device afterwards with
`curl -X DELETE $RADIX/v1/devices/<device_id> -H "authorization: Bearer <device_secret>"`.

## Try it with an agent

Until the app exists, you can play the app's part with `curl` and point an
agent at Radix:

```sh
# register a device (a real token from a development build, or a fake one)
curl -s $RADIX/v1/devices -H 'content-type: application/json' \
  -d '{"apns_token":"<hex>","environment":"sandbox"}'
# link a machine; the machine ID is in `hedera ls --json` or ~/.config/hedera/config.json
curl -s $RADIX/v1/machines -H "authorization: Bearer <device_secret>" \
  -H 'content-type: application/json' -d '{"machine_id":"m_…","label":"studio"}'
# hand the push token to the agent, then send a test notification
hedera push configure --radix $RADIX --token hrp_…
hedera push test
```
