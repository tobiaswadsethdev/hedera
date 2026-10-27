import { env } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apnsOrigin, providerToken, resetApnsCache } from "../src/apns";
import * as mainModule from "../src/index";
import worker from "../src/index";

// --- helpers --------------------------------------------------------------

const APNS_TOKEN = "a".repeat(64);
const MACHINE = "m_abcdefghijklmnop";

interface ApnsCall {
  url: string;
  headers: Headers;
  body: any;
}

let apnsCalls: ApnsCall[] = [];
let apnsReplies: Array<Response | Error> = [];

beforeEach(() => {
  apnsCalls = [];
  apnsReplies = [];
  resetApnsCache();
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const req = new Request(input as RequestInfo, init);
    apnsCalls.push({ url: req.url, headers: req.headers, body: await req.json() });
    const next = apnsReplies.shift() ?? new Response(null, { status: 200, headers: { "apns-id": "id-1" } });
    if (next instanceof Error) throw next;
    return next;
  });
});

afterEach(() => vi.restoreAllMocks());

function apnsError(status: number, reason: string) {
  return new Response(JSON.stringify({ reason }), { status });
}

async function call(method: string, path: string, opts: { auth?: string; body?: unknown; ip?: string } = {}) {
  const headers: Record<string, string> = { "cf-connecting-ip": opts.ip ?? "203.0.113.1" };
  if (opts.auth) headers.authorization = `Bearer ${opts.auth}`;
  if (opts.body !== undefined) headers["content-type"] = "application/json";
  const res = await worker.fetch(
    new Request(`https://radix.test${path}`, {
      method,
      headers,
      body: opts.body === undefined ? undefined : typeof opts.body === "string" ? opts.body : JSON.stringify(opts.body),
    }),
    env,
  );
  const text = await res.text();
  return { status: res.status, headers: res.headers, json: text ? JSON.parse(text) : null };
}

let ipCounter = 0;
async function device(environment = "sandbox") {
  const r = await call("POST", "/v1/devices", {
    body: { apns_token: APNS_TOKEN, environment },
    ip: `198.51.100.${++ipCounter % 250}`,
  });
  expect(r.status).toBe(201);
  return r.json as { device_id: string; device_secret: string };
}

async function link(secret: string, machineId = MACHINE, label = "studio") {
  const r = await call("POST", "/v1/machines", { auth: secret, body: { machine_id: machineId, label } });
  expect(r.status).toBe(201);
  return r.json as { link_id: string; push_token: string };
}

let eventCounter = 0;
function event(extra: Record<string, unknown> = {}) {
  return {
    event_id: `b_testboot01-${++eventCounter}`,
    kind: "claude_needs_input",
    title: "fix flaky test",
    subtitle: "studio",
    body: "Claude needs your permission to use Bash",
    session_id: "s_4fj7q2m3xa",
    turn: 7,
    ...extra,
  };
}

function b64urlDecode(s: string): Uint8Array {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/");
  return Uint8Array.from(atob(b64 + "=".repeat((4 - (b64.length % 4)) % 4)), (c) => c.charCodeAt(0));
}

async function verifyJWT(jwt: string) {
  const [h, c, s] = jwt.split(".");
  const key = await crypto.subtle.importKey(
    "jwk",
    JSON.parse(env.TEST_APNS_PUBLIC_JWK),
    { name: "ECDSA", namedCurve: "P-256" },
    false,
    ["verify"],
  );
  const ok = await crypto.subtle.verify(
    { name: "ECDSA", hash: "SHA-256" },
    key,
    b64urlDecode(s),
    new TextEncoder().encode(`${h}.${c}`),
  );
  const dec = (x: string) => JSON.parse(new TextDecoder().decode(b64urlDecode(x)));
  return { ok, header: dec(h), claims: dec(c) };
}

// --- tests ----------------------------------------------------------------

describe("basics", () => {
  it("exports only the default handler from the main module", () => {
    // workerd refuses to start a Worker whose main module exports anything else.
    expect(Object.keys(mainModule)).toEqual(["default"]);
  });

  it("reports health", async () => {
    const r = await call("GET", "/v1/health");
    expect(r).toMatchObject({ status: 200, json: { ok: true } });
  });

  it("404s unknown routes and wrong methods", async () => {
    expect((await call("GET", "/v1/nope")).status).toBe(404);
    expect((await call("GET", "/v1/events")).status).toBe(404);
  });

  it("rejects bodies that are not JSON objects", async () => {
    for (const body of ["{", "[]", "1"]) {
      const r = await call("POST", "/v1/devices", { body, ip: "192.0.2.77" });
      expect(r.status).toBe(400);
      expect(r.json.error.code).toBe("bad_request");
    }
  });
});

describe("devices", () => {
  it("registers and returns credentials", async () => {
    const d = await device();
    expect(d.device_id).toMatch(/^d_[a-z2-7]{16}$/);
    expect(d.device_secret).toMatch(/^hrd_[A-Za-z0-9_-]{43}$/);
    // Only the hash is stored.
    const row = await env.DB.prepare("SELECT * FROM devices WHERE id = ?").bind(d.device_id).first<Record<string, string>>();
    expect(JSON.stringify(row)).not.toContain(d.device_secret);
  });

  it("validates device fields", async () => {
    for (const body of [
      { apns_token: "zz", environment: "sandbox" },
      { apns_token: APNS_TOKEN, environment: "staging" },
      { environment: "sandbox" },
    ]) {
      expect((await call("POST", "/v1/devices", { body, ip: "192.0.2.10" })).status).toBe(400);
    }
  });

  it("rate limits registrations per address", async () => {
    const body = { apns_token: APNS_TOKEN, environment: "sandbox" };
    for (let i = 0; i < 10; i++) expect((await call("POST", "/v1/devices", { body, ip: "192.0.2.50" })).status).toBe(201);
    const limited = await call("POST", "/v1/devices", { body, ip: "192.0.2.50" });
    expect(limited.status).toBe(429);
    expect(limited.headers.get("retry-after")).toBe("3600");
    expect((await call("POST", "/v1/devices", { body, ip: "192.0.2.51" })).status).toBe(201);
  });

  it("only lets a device manage itself", async () => {
    const a = await device();
    const b = await device();
    const body = { apns_token: "b".repeat(64), environment: "production" };
    expect((await call("PUT", `/v1/devices/${a.device_id}`, { auth: b.device_secret, body })).status).toBe(401);
    expect((await call("PUT", `/v1/devices/${a.device_id}`, { auth: "hrd_nope", body })).status).toBe(401);
    expect((await call("PUT", `/v1/devices/${a.device_id}`, { body })).status).toBe(401);
    expect((await call("PUT", `/v1/devices/${a.device_id}`, { auth: a.device_secret, body })).status).toBe(200);
  });

  it("deleting a device removes its links", async () => {
    const d = await device();
    const l = await link(d.device_secret);
    expect((await call("DELETE", `/v1/devices/${d.device_id}`, { auth: d.device_secret })).status).toBe(204);
    expect((await call("GET", "/v1/link", { auth: l.push_token })).status).toBe(401);
  });
});

describe("links", () => {
  it("creates, lists and deletes links", async () => {
    const d = await device();
    const l = await link(d.device_secret);
    expect(l.link_id).toMatch(/^l_[a-z2-7]{16}$/);
    expect(l.push_token).toMatch(/^hrp_/);

    const list = await call("GET", "/v1/machines", { auth: d.device_secret });
    expect(list.json.links).toEqual([
      { link_id: l.link_id, machine_id: MACHINE, label: "studio", created_at: expect.any(String), last_event_at: null },
    ]);

    const check = await call("GET", "/v1/link", { auth: l.push_token });
    expect(check.json).toEqual({ link_id: l.link_id, label: "studio", device_enabled: true });

    // Another device can't delete it.
    const other = await device();
    expect((await call("DELETE", `/v1/machines/${l.link_id}`, { auth: other.device_secret })).status).toBe(404);
    expect((await call("DELETE", `/v1/machines/${l.link_id}`, { auth: d.device_secret })).status).toBe(204);
    expect((await call("GET", "/v1/link", { auth: l.push_token })).status).toBe(401);
  });

  it("validates link fields and credentials", async () => {
    const d = await device();
    expect((await call("POST", "/v1/machines", { auth: d.device_secret, body: { machine_id: "studio", label: "x" } })).status).toBe(400);
    // A push token is not a device secret, and vice versa.
    const l = await link(d.device_secret);
    expect((await call("GET", "/v1/machines", { auth: l.push_token })).status).toBe(401);
    expect((await call("GET", "/v1/link", { auth: d.device_secret })).status).toBe(401);
  });
});

describe("events", () => {
  it("sends a notification with the payload Radix decides", async () => {
    const d = await device("production");
    const l = await link(d.device_secret);
    const r = await call("POST", "/v1/events", {
      auth: l.push_token,
      body: event({ machine_id: "m_spoofedspoofedsp" }),
    });
    expect(r).toMatchObject({ status: 200, json: { sent: true } });

    expect(apnsCalls).toHaveLength(1);
    const c = apnsCalls[0];
    expect(c.url).toBe(`https://api.push.apple.com/3/device/${APNS_TOKEN}`);
    expect(c.headers.get("apns-topic")).toBe("dev.hedera.test");
    expect(c.headers.get("apns-push-type")).toBe("alert");
    expect(c.headers.get("apns-priority")).toBe("10");
    expect(Number(c.headers.get("apns-expiration"))).toBeGreaterThan(Date.now() / 1000 + 5 * 3600);
    expect(c.body).toEqual({
      aps: {
        alert: { title: "fix flaky test", subtitle: "studio", body: "Claude needs your permission to use Bash" },
        sound: "default",
        category: "CLAUDE_INPUT",
        "thread-id": `${MACHINE}:s_4fj7q2m3xa`,
        "interruption-level": "active",
      },
      // machine_id comes from the link, never from the agent.
      hedera: { kind: "claude_needs_input", machine_id: MACHINE, link_id: l.link_id, session_id: "s_4fj7q2m3xa", turn: 7 },
    });

    const auth = c.headers.get("authorization")!;
    expect(auth).toMatch(/^bearer /);
    const jwt = await verifyJWT(auth.slice(7));
    expect(jwt.ok).toBe(true);
    expect(jwt.header).toEqual({ alg: "ES256", kid: "TESTKEY123" });
    expect(jwt.claims.iss).toBe("TESTTEAM12");

    const list = await call("GET", "/v1/machines", { auth: d.device_secret });
    expect(list.json.links[0].last_event_at).not.toBeNull();
  });

  it("maps kinds to categories and interruption levels", async () => {
    const d = await device();
    const l = await link(d.device_secret);
    const expected: Record<string, [string, string]> = {
      shell_exited: ["SHELL_EXITED", "time-sensitive"],
      claude_turn_finished: ["CLAUDE_TURN", "active"],
      claude_needs_input: ["CLAUDE_INPUT", "active"],
      claude_failed: ["CLAUDE_FAILED", "time-sensitive"],
      claude_ended: ["CLAUDE_ENDED", "active"],
      claude_ended_abnormally: ["CLAUDE_ENDED", "time-sensitive"],
      test: ["TEST", "active"],
    };
    for (const [kind, [category, level]] of Object.entries(expected)) {
      apnsCalls = [];
      expect((await call("POST", "/v1/events", { auth: l.push_token, body: event({ kind }) })).status).toBe(200);
      expect(apnsCalls[0].body.aps.category).toBe(category);
      expect(apnsCalls[0].body.aps["interruption-level"]).toBe(level);
      // Sandbox devices go to the sandbox host.
      expect(apnsCalls[0].url).toMatch(/^https:\/\/api\.sandbox\.push\.apple\.com\//);
    }
  });

  it("validates events", async () => {
    const d = await device();
    const l = await link(d.device_secret);
    for (const bad of [
      { kind: "made_up" },
      { session_id: "s_bad" },
      { turn: -1 },
      { turn: 1.5 },
      { title: 42 },
      { event_id: "has spaces" },
    ]) {
      expect((await call("POST", "/v1/events", { auth: l.push_token, body: event(bad) })).status).toBe(400);
    }
    expect((await call("POST", "/v1/events", { auth: "hrp_unknown", body: event() })).status).toBe(401);
    expect(apnsCalls).toHaveLength(0);
  });

  it("clips long text and omits optional fields", async () => {
    const d = await device();
    const l = await link(d.device_secret);
    const long = "ä".repeat(900);
    await call("POST", "/v1/events", {
      auth: l.push_token,
      body: { event_id: "e1", kind: "test", title: long, body: long },
    });
    const { alert } = apnsCalls[0].body.aps;
    expect(Array.from(alert.title)).toHaveLength(100);
    expect(Array.from(alert.body)).toHaveLength(500);
    expect(alert.body.endsWith("…")).toBe(true);
    expect(alert.subtitle).toBeUndefined();
    expect(apnsCalls[0].body.hedera).toEqual({ kind: "test", machine_id: MACHINE, link_id: l.link_id });
    expect(apnsCalls[0].body.aps["thread-id"]).toBe(MACHINE);
  });

  it("acknowledges duplicates without sending again", async () => {
    const d = await device();
    const l = await link(d.device_secret);
    const e = event();
    expect((await call("POST", "/v1/events", { auth: l.push_token, body: e })).json).toEqual({ sent: true });
    expect((await call("POST", "/v1/events", { auth: l.push_token, body: e })).json).toEqual({ sent: false, reason: "duplicate" });
    expect(apnsCalls).toHaveLength(1);
  });

  it("rate limits events per link", async () => {
    const d = await device();
    const l = await link(d.device_secret);
    for (let i = 0; i < 30; i++) expect((await call("POST", "/v1/events", { auth: l.push_token, body: event() })).status).toBe(200);
    const r = await call("POST", "/v1/events", { auth: l.push_token, body: event() });
    expect(r.status).toBe(429);
    expect(r.headers.get("retry-after")).toBe("60");
    // Other links are unaffected.
    const l2 = await link(d.device_secret, "m_qrstuvwxyzabcdef");
    expect((await call("POST", "/v1/events", { auth: l2.push_token, body: event() })).status).toBe(200);
  });

  it("disables a device APNs no longer knows, until the app updates it", async () => {
    const d = await device();
    const l = await link(d.device_secret);
    apnsReplies.push(apnsError(410, "Unregistered"));
    expect((await call("POST", "/v1/events", { auth: l.push_token, body: event() })).json).toEqual({
      sent: false,
      reason: "device_unregistered",
    });
    expect((await call("POST", "/v1/events", { auth: l.push_token, body: event() })).json.reason).toBe("device_unregistered");
    expect(apnsCalls).toHaveLength(1);
    expect((await call("GET", "/v1/link", { auth: l.push_token })).json.device_enabled).toBe(false);

    await call("PUT", `/v1/devices/${d.device_id}`, {
      auth: d.device_secret,
      body: { apns_token: "c".repeat(64), environment: "sandbox" },
    });
    expect((await call("POST", "/v1/events", { auth: l.push_token, body: event() })).json).toEqual({ sent: true });
    expect(apnsCalls[1].url).toContain("c".repeat(64));
  });

  it("asks the agent to retry when APNs fails, and the retry goes through", async () => {
    const d = await device();
    const l = await link(d.device_secret);
    const e = event();
    apnsReplies.push(apnsError(503, "ServiceUnavailable"));
    const r = await call("POST", "/v1/events", { auth: l.push_token, body: e });
    expect(r.status).toBe(502);
    expect(r.json.error.code).toBe("apns_error");
    expect(r.json.error.message).toContain("ServiceUnavailable");

    apnsReplies.push(new Error("network down"));
    expect((await call("POST", "/v1/events", { auth: l.push_token, body: e })).status).toBe(502);

    expect((await call("POST", "/v1/events", { auth: l.push_token, body: e })).json).toEqual({ sent: true });
    expect(apnsCalls).toHaveLength(3);
  });

  it("refreshes an expired provider token once", async () => {
    const d = await device();
    const l = await link(d.device_secret);
    // Two sends with the same cached token...
    await call("POST", "/v1/events", { auth: l.push_token, body: event() });
    await call("POST", "/v1/events", { auth: l.push_token, body: event() });
    expect(apnsCalls[0].headers.get("authorization")).toBe(apnsCalls[1].headers.get("authorization"));
    // ...then APNs rejects it: a fresh token is minted and the send retried.
    apnsReplies.push(apnsError(403, "ExpiredProviderToken"));
    expect((await call("POST", "/v1/events", { auth: l.push_token, body: event() })).json).toEqual({ sent: true });
    expect(apnsCalls).toHaveLength(4);
  });
});

describe("device test route", () => {
  it("reports what APNs said, which doubles as the connectivity check", async () => {
    const d = await device();
    apnsReplies.push(apnsError(400, "BadDeviceToken"));
    const r = await call("POST", `/v1/devices/${d.device_id}/test`, { auth: d.device_secret });
    expect(r.json).toEqual({ sent: false, apns_status: 400, apns_id: null, apns_reason: "BadDeviceToken" });
    expect(apnsCalls[0].body.aps.category).toBe("TEST");

    const ok = await call("POST", `/v1/devices/${d.device_id}/test`, { auth: d.device_secret });
    expect(ok.json).toMatchObject({ sent: true, apns_status: 200, apns_id: "id-1" });
  });
});

describe("apns", () => {
  it("reuses provider tokens for 50 minutes", async () => {
    const t0 = Date.UTC(2026, 9, 27, 12);
    const a = await providerToken(env, t0);
    expect(await providerToken(env, t0 + 49 * 60 * 1000)).toBe(a);
    const b = await providerToken(env, t0 + 51 * 60 * 1000);
    expect(b).not.toBe(a);
    expect((await verifyJWT(b)).claims.iat).toBe(Math.floor((t0 + 51 * 60 * 1000) / 1000));
  });

  it("only accepts a loopback origin override", () => {
    const withOverride = (o: string) => apnsOrigin({ ...env, APNS_ORIGIN_OVERRIDE: o }, "production");
    expect(withOverride("http://127.0.0.1:9999")).toBe("http://127.0.0.1:9999");
    expect(withOverride("http://localhost:8080")).toBe("http://localhost:8080");
    expect(withOverride("https://evil.example")).toBe("https://api.push.apple.com");
    expect(withOverride("http://127.0.0.1.evil.example")).toBe("https://api.push.apple.com");
    expect(withOverride("")).toBe("https://api.push.apple.com");
  });
});

describe("cleanup", () => {
  it("prunes old idempotency and registration rows", async () => {
    const d = await device();
    const l = await link(d.device_secret);
    const old = Date.now() - 25 * 3600 * 1000;
    await env.DB.prepare("INSERT INTO events (link_id, event_id, created_at) VALUES (?, 'old', ?)").bind(l.link_id, old).run();
    await env.DB.prepare("INSERT INTO registrations (ip, at) VALUES ('x', ?)").bind(old).run();
    await worker.scheduled({} as ScheduledController, env);
    expect(await env.DB.prepare("SELECT COUNT(*) AS n FROM events WHERE event_id = 'old'").first("n")).toBe(0);
    expect(await env.DB.prepare("SELECT COUNT(*) AS n FROM registrations WHERE ip = 'x'").first("n")).toBe(0);
  });
});
