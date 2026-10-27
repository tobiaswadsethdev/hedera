// APNs client: ES256 provider tokens signed with WebCrypto, and HTTP/2 sends.

import { base64url, type RadixEnv } from "./util";

const HOSTS = {
  production: "https://api.push.apple.com",
  sandbox: "https://api.sandbox.push.apple.com",
} as const;

export type Environment = keyof typeof HOSTS;

// Apple accepts a provider token for an hour and rejects refreshing it more
// often than every 20 minutes; 50 minutes sits safely in between.
const TOKEN_LIFETIME_MS = 50 * 60 * 1000;

let cachedKey: { pem: string; key: CryptoKey } | null = null;
let cachedToken: { jwt: string; issuedAt: number; keyId: string; teamId: string } | null = null;

/** For tests: forget cached keys and tokens. */
export function resetApnsCache() {
  cachedKey = null;
  cachedToken = null;
}

async function signingKey(pem: string): Promise<CryptoKey> {
  if (cachedKey?.pem === pem) return cachedKey.key;
  const b64 = pem.replace(/-----(BEGIN|END) PRIVATE KEY-----/g, "").replace(/\s+/g, "");
  const der = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
  const key = await crypto.subtle.importKey("pkcs8", der, { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]);
  cachedKey = { pem, key };
  return key;
}

export async function providerToken(env: RadixEnv, now = Date.now()): Promise<string> {
  if (!env.APNS_KEY_P8) throw new Error("APNS_KEY_P8 is not configured");
  const c = cachedToken;
  if (c && c.keyId === env.APNS_KEY_ID && c.teamId === env.APNS_TEAM_ID && now - c.issuedAt < TOKEN_LIFETIME_MS) {
    return c.jwt;
  }
  const header = base64url(JSON.stringify({ alg: "ES256", kid: env.APNS_KEY_ID }));
  const claims = base64url(JSON.stringify({ iss: env.APNS_TEAM_ID, iat: Math.floor(now / 1000) }));
  const input = `${header}.${claims}`;
  // WebCrypto's ECDSA signature is the raw r||s form JWS expects.
  const sig = await crypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" },
    await signingKey(env.APNS_KEY_P8),
    new TextEncoder().encode(input),
  );
  const jwt = `${input}.${base64url(sig)}`;
  cachedToken = { jwt, issuedAt: now, keyId: env.APNS_KEY_ID, teamId: env.APNS_TEAM_ID };
  return jwt;
}

/** APNs origin for an environment, or the loopback override used in tests. */
export function apnsOrigin(env: RadixEnv, environment: Environment): string {
  const o = env.APNS_ORIGIN_OVERRIDE;
  if (o && /^http:\/\/(127\.0\.0\.1|localhost)(:\d+)?$/.test(o)) return o;
  return HOSTS[environment];
}

export interface ApnsResult {
  status: number; // 0 when the request never got an answer
  apnsId?: string;
  reason?: string;
}

/** APNs reasons meaning the device token will never work again. */
export const DEAD_TOKEN_REASONS = new Set(["BadDeviceToken", "Unregistered", "DeviceTokenNotForTopic"]);

export async function sendApns(
  env: RadixEnv,
  device: { token: string; environment: Environment },
  payload: unknown,
  expiresInSeconds = 6 * 3600,
): Promise<ApnsResult> {
  const url = `${apnsOrigin(env, device.environment)}/3/device/${device.token}`;
  const body = JSON.stringify(payload);
  for (let attempt = 0; attempt < 2; attempt++) {
    let resp: Response;
    try {
      resp = await fetch(url, {
        method: "POST",
        headers: {
          authorization: `bearer ${await providerToken(env)}`,
          "apns-topic": env.APNS_TOPIC,
          "apns-push-type": "alert",
          "apns-priority": "10",
          "apns-expiration": String(Math.floor(Date.now() / 1000) + expiresInSeconds),
          "content-type": "application/json",
        },
        body,
      });
    } catch (e) {
      return { status: 0, reason: e instanceof Error ? e.message : "fetch failed" };
    }
    const apnsId = resp.headers.get("apns-id") ?? undefined;
    if (resp.status === 200) {
      await resp.body?.cancel();
      return { status: 200, apnsId };
    }
    let reason: string | undefined;
    try {
      reason = ((await resp.json()) as { reason?: string }).reason;
    } catch {
      reason = undefined;
    }
    // A stale or rejected provider token: mint a fresh one and try once more.
    if (resp.status === 403 && attempt === 0 && (reason === "ExpiredProviderToken" || reason === "InvalidProviderToken")) {
      cachedToken = null;
      continue;
    }
    return { status: resp.status, apnsId, reason };
  }
  return { status: 0, reason: "unreachable" };
}
