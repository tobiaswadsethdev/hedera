// Small helpers shared by the routes.

export type RadixEnv = Env & {
  // Secret, set with `wrangler secret put APNS_KEY_P8`.
  APNS_KEY_P8?: string;
};

export class HttpError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly headers: Record<string, string> = {},
  ) {
    super(message);
  }
}

export const badRequest = (msg: string) => new HttpError(400, "bad_request", msg);
export const unauthorized = () => new HttpError(401, "unauthorized", "missing or invalid credential");
export const notFound = () => new HttpError(404, "not_found", "not found");

export function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(body === null ? null : JSON.stringify(body), {
    status,
    headers: body === null ? headers : { "content-type": "application/json", ...headers },
  });
}

const MAX_BODY = 16 * 1024;

export async function readJSON(req: Request): Promise<Record<string, unknown>> {
  const text = await req.text();
  if (text.length > MAX_BODY) throw badRequest("body too large");
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    throw badRequest("body is not valid JSON");
  }
  if (typeof v !== "object" || v === null || Array.isArray(v)) throw badRequest("body must be a JSON object");
  return v as Record<string, unknown>;
}

export function bearer(req: Request): string | null {
  const h = req.headers.get("authorization") ?? "";
  const m = /^Bearer\s+(\S+)$/i.exec(h);
  return m ? m[1] : null;
}

const b64url = (bytes: Uint8Array) =>
  btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

export function base64url(data: ArrayBuffer | Uint8Array | string): string {
  if (typeof data === "string") return b64url(new TextEncoder().encode(data));
  return b64url(data instanceof Uint8Array ? data : new Uint8Array(data));
}

/** A secret: prefix + 32 random bytes, base64url. */
export function newSecret(prefix: string): string {
  return prefix + base64url(crypto.getRandomValues(new Uint8Array(32)));
}

const B32 = "abcdefghijklmnopqrstuvwxyz234567";

/** An ID: prefix + n random base32 characters. */
export function newId(prefix: string, n = 16): string {
  const bytes = crypto.getRandomValues(new Uint8Array(n));
  let s = prefix;
  for (const b of bytes) s += B32[b & 31];
  return s;
}

export async function sha256hex(s: string): Promise<string> {
  const d = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s));
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

/** Truncates to at most n code points, marking the cut with an ellipsis. */
export function clip(s: string, n: number): string {
  const cps = Array.from(s);
  return cps.length <= n ? s : cps.slice(0, n - 1).join("") + "…";
}

export const iso = (ms: number | null) => (ms == null ? null : new Date(ms).toISOString());
