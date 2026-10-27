// Radix: the Hedera push relay. See protocol/radix-api.md.

import { DEAD_TOKEN_REASONS, sendApns, type Environment } from "./apns";
import { apnsPayload, KINDS, type Kind } from "./notify";
import {
  badRequest,
  bearer,
  HttpError,
  iso,
  json,
  newId,
  newSecret,
  notFound,
  readJSON,
  sha256hex,
  unauthorized,
  type RadixEnv,
  VERSION,
} from "./util";

// Only handlers may be exported from this module: workerd treats every
// export of the main module as an entrypoint and refuses to start otherwise.

const REGISTRATIONS_PER_HOUR = 10;
const EVENTS_PER_MINUTE = 30;
const LINKS_PER_DEVICE = 50;
const DAY_MS = 24 * 3600 * 1000;

interface DeviceRow {
  id: string;
  apns_token: string;
  environment: Environment;
  disabled_at: number | null;
}

interface LinkRow {
  id: string;
  device_id: string;
  machine_id: string;
  label: string;
  created_at: number;
  last_event_at: number | null;
}

type Handler = (req: Request, env: RadixEnv, params: Record<string, string>) => Promise<Response>;

const routes: Array<[string, RegExp, Handler]> = [
  ["GET", /^\/v1\/health$/, async () => json(200, { ok: true, version: VERSION })],
  ["POST", /^\/v1\/devices$/, registerDevice],
  ["PUT", /^\/v1\/devices\/(?<device>d_[a-z2-7]{16})$/, updateDevice],
  ["DELETE", /^\/v1\/devices\/(?<device>d_[a-z2-7]{16})$/, deleteDevice],
  ["POST", /^\/v1\/devices\/(?<device>d_[a-z2-7]{16})\/test$/, testDevice],
  ["POST", /^\/v1\/machines$/, createLink],
  ["GET", /^\/v1\/machines$/, listLinks],
  ["DELETE", /^\/v1\/machines\/(?<link>l_[a-z2-7]{16})$/, deleteLink],
  ["GET", /^\/v1\/link$/, checkLink],
  ["POST", /^\/v1\/events$/, postEvent],
];

export default {
  async fetch(req: Request, env: RadixEnv): Promise<Response> {
    const path = new URL(req.url).pathname;
    try {
      for (const [method, re, handler] of routes) {
        const m = re.exec(path);
        if (m && req.method === method) return await handler(req, env, m.groups ?? {});
      }
      throw notFound();
    } catch (e) {
      if (e instanceof HttpError) return json(e.status, { error: { code: e.code, message: e.message } }, e.headers);
      console.error(JSON.stringify({ msg: "unhandled error", path, error: String(e) }));
      return json(500, { error: { code: "internal", message: "internal error" } });
    }
  },

  async scheduled(_ctrl: ScheduledController, env: RadixEnv): Promise<void> {
    const now = Date.now();
    await env.DB.batch([
      env.DB.prepare("DELETE FROM events WHERE created_at < ?").bind(now - DAY_MS),
      env.DB.prepare("DELETE FROM registrations WHERE at < ?").bind(now - 3600 * 1000),
    ]);
  },
} satisfies ExportedHandler<RadixEnv>;

// --- credentials --------------------------------------------------------

async function authDevice(req: Request, env: RadixEnv, pathId?: string): Promise<DeviceRow> {
  const secret = bearer(req);
  if (!secret?.startsWith("hrd_")) throw unauthorized();
  const row = await env.DB.prepare("SELECT id, apns_token, environment, disabled_at FROM devices WHERE secret_hash = ?")
    .bind(await sha256hex(secret))
    .first<DeviceRow>();
  // A valid secret for another device is as good as no secret here.
  if (!row || (pathId !== undefined && row.id !== pathId)) throw unauthorized();
  return row;
}

async function authLink(req: Request, env: RadixEnv): Promise<LinkRow & { device: DeviceRow }> {
  const token = bearer(req);
  if (!token?.startsWith("hrp_")) throw unauthorized();
  const row = await env.DB.prepare(
    `SELECT l.id, l.device_id, l.machine_id, l.label, l.created_at, l.last_event_at,
            d.apns_token, d.environment, d.disabled_at
       FROM links l JOIN devices d ON d.id = l.device_id
      WHERE l.token_hash = ?`,
  )
    .bind(await sha256hex(token))
    .first<LinkRow & Omit<DeviceRow, "id">>();
  if (!row) throw unauthorized();
  return {
    ...row,
    device: { id: row.device_id, apns_token: row.apns_token, environment: row.environment, disabled_at: row.disabled_at },
  };
}

// --- validation ---------------------------------------------------------

function str(body: Record<string, unknown>, key: string, opts: { max: number; re?: RegExp; optional?: boolean }) {
  const v = body[key];
  if (v === undefined || v === null) {
    if (opts.optional) return undefined;
    throw badRequest(`${key} is required`);
  }
  if (typeof v !== "string") throw badRequest(`${key} must be a string`);
  if (opts.re && !opts.re.test(v)) throw badRequest(`${key} is malformed`);
  if (v.length > opts.max) throw badRequest(`${key} is too long`);
  return v;
}

function deviceFields(body: Record<string, unknown>) {
  const token = str(body, "apns_token", { max: 200, re: /^[0-9a-fA-F]{64,200}$/ })!.toLowerCase();
  const environment = str(body, "environment", { max: 20, re: /^(sandbox|production)$/ }) as Environment;
  return { token, environment };
}

// --- device routes ------------------------------------------------------

async function registerDevice(req: Request, env: RadixEnv): Promise<Response> {
  const ip = req.headers.get("cf-connecting-ip") ?? "unknown";
  const now = Date.now();
  const recent = await env.DB.prepare("SELECT COUNT(*) AS n FROM registrations WHERE ip = ? AND at > ?")
    .bind(ip, now - 3600 * 1000)
    .first<{ n: number }>();
  if ((recent?.n ?? 0) >= REGISTRATIONS_PER_HOUR) {
    throw new HttpError(429, "rate_limited", "too many registrations", { "retry-after": "3600" });
  }
  const { token, environment } = deviceFields(await readJSON(req));
  const id = newId("d_");
  const secret = newSecret("hrd_");
  await env.DB.batch([
    env.DB.prepare(
      "INSERT INTO devices (id, secret_hash, apns_token, environment, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
    ).bind(id, await sha256hex(secret), token, environment, now, now),
    env.DB.prepare("INSERT INTO registrations (ip, at) VALUES (?, ?)").bind(ip, now),
  ]);
  return json(201, { device_id: id, device_secret: secret });
}

async function updateDevice(req: Request, env: RadixEnv, p: Record<string, string>): Promise<Response> {
  const dev = await authDevice(req, env, p.device);
  const { token, environment } = deviceFields(await readJSON(req));
  await env.DB.prepare("UPDATE devices SET apns_token = ?, environment = ?, updated_at = ?, disabled_at = NULL WHERE id = ?")
    .bind(token, environment, Date.now(), dev.id)
    .run();
  return json(200, { device_id: dev.id });
}

async function deleteDevice(req: Request, env: RadixEnv, p: Record<string, string>): Promise<Response> {
  const dev = await authDevice(req, env, p.device);
  // D1 enforces foreign keys, so links and their events go with the device.
  await env.DB.prepare("DELETE FROM devices WHERE id = ?").bind(dev.id).run();
  return json(204, null);
}

async function testDevice(req: Request, env: RadixEnv, p: Record<string, string>): Promise<Response> {
  const dev = await authDevice(req, env, p.device);
  const payload = apnsPayload(
    { kind: "test", title: "Hedera", body: "Test notification from Radix" },
    { id: "l_test", machineId: "radix" },
  );
  const r = await sendApns(env, { token: dev.apns_token, environment: dev.environment }, payload);
  if (r.status === 200 && dev.disabled_at !== null) {
    await env.DB.prepare("UPDATE devices SET disabled_at = NULL WHERE id = ?").bind(dev.id).run();
  } else if (r.reason && DEAD_TOKEN_REASONS.has(r.reason)) {
    await env.DB.prepare("UPDATE devices SET disabled_at = ? WHERE id = ?").bind(Date.now(), dev.id).run();
  }
  log("device test", { device: dev.id, apns_status: r.status, apns_reason: r.reason });
  return json(200, { sent: r.status === 200, apns_status: r.status, apns_id: r.apnsId ?? null, apns_reason: r.reason ?? null });
}

// --- link routes --------------------------------------------------------

async function createLink(req: Request, env: RadixEnv): Promise<Response> {
  const dev = await authDevice(req, env);
  const body = await readJSON(req);
  const machineId = str(body, "machine_id", { max: 64, re: /^m_[a-z2-7]{16}$/ })!;
  const label = str(body, "label", { max: 100 })!.trim() || machineId;
  const count = await env.DB.prepare("SELECT COUNT(*) AS n FROM links WHERE device_id = ?").bind(dev.id).first<{ n: number }>();
  if ((count?.n ?? 0) >= LINKS_PER_DEVICE) throw badRequest(`a device can have at most ${LINKS_PER_DEVICE} links`);
  const id = newId("l_");
  const token = newSecret("hrp_");
  await env.DB.prepare("INSERT INTO links (id, device_id, machine_id, label, token_hash, created_at) VALUES (?, ?, ?, ?, ?, ?)")
    .bind(id, dev.id, machineId, label, await sha256hex(token), Date.now())
    .run();
  return json(201, { link_id: id, push_token: token });
}

async function listLinks(req: Request, env: RadixEnv): Promise<Response> {
  const dev = await authDevice(req, env);
  const { results } = await env.DB.prepare(
    "SELECT id, machine_id, label, created_at, last_event_at FROM links WHERE device_id = ? ORDER BY created_at",
  )
    .bind(dev.id)
    .all<LinkRow>();
  return json(200, {
    links: results.map((l) => ({
      link_id: l.id,
      machine_id: l.machine_id,
      label: l.label,
      created_at: iso(l.created_at),
      last_event_at: iso(l.last_event_at),
    })),
  });
}

async function deleteLink(req: Request, env: RadixEnv, p: Record<string, string>): Promise<Response> {
  const dev = await authDevice(req, env);
  const r = await env.DB.prepare("DELETE FROM links WHERE id = ? AND device_id = ?").bind(p.link, dev.id).run();
  if (!r.meta.changes) throw notFound();
  return json(204, null);
}

// --- agent routes -------------------------------------------------------

async function checkLink(req: Request, env: RadixEnv): Promise<Response> {
  const link = await authLink(req, env);
  return json(200, { link_id: link.id, label: link.label, device_enabled: link.device.disabled_at === null });
}

async function postEvent(req: Request, env: RadixEnv): Promise<Response> {
  const link = await authLink(req, env);
  const body = await readJSON(req);
  const eventId = str(body, "event_id", { max: 100, re: /^[A-Za-z0-9_.:-]+$/ })!;
  const kind = str(body, "kind", { max: 40 })!;
  if (!(kind in KINDS)) throw badRequest(`unknown kind ${JSON.stringify(kind)}`);
  const title = str(body, "title", { max: 1000 })!;
  const subtitle = str(body, "subtitle", { max: 1000, optional: true });
  const text = str(body, "body", { max: 4000 })!;
  const sessionId = str(body, "session_id", { max: 20, re: /^s_[a-z2-7]{10}$/, optional: true });
  let turn: number | undefined;
  if (body.turn !== undefined && body.turn !== null) {
    if (!Number.isInteger(body.turn) || (body.turn as number) < 0) throw badRequest("turn must be a non-negative integer");
    turn = body.turn as number;
  }

  const now = Date.now();
  const recent = await env.DB.prepare("SELECT COUNT(*) AS n FROM events WHERE link_id = ? AND created_at > ?")
    .bind(link.id, now - 60 * 1000)
    .first<{ n: number }>();
  if ((recent?.n ?? 0) >= EVENTS_PER_MINUTE) {
    throw new HttpError(429, "rate_limited", "too many events", { "retry-after": "60" });
  }
  const ins = await env.DB.prepare("INSERT OR IGNORE INTO events (link_id, event_id, created_at) VALUES (?, ?, ?)")
    .bind(link.id, eventId, now)
    .run();
  if (!ins.meta.changes) return json(200, { sent: false, reason: "duplicate" });
  if (link.device.disabled_at !== null) return json(200, { sent: false, reason: "device_unregistered" });

  const payload = apnsPayload({ kind: kind as Kind, title, subtitle, body: text, sessionId, turn }, { id: link.id, machineId: link.machine_id });
  const r = await sendApns(env, { token: link.device.apns_token, environment: link.device.environment }, payload);
  log("event", { link: link.id, kind, apns_status: r.status, apns_reason: r.reason });

  if (r.status === 200) {
    await env.DB.prepare("UPDATE links SET last_event_at = ? WHERE id = ?").bind(now, link.id).run();
    return json(200, { sent: true });
  }
  if (r.reason && DEAD_TOKEN_REASONS.has(r.reason)) {
    await env.DB.prepare("UPDATE devices SET disabled_at = ? WHERE id = ?").bind(now, link.device.id).run();
    return json(200, { sent: false, reason: "device_unregistered" });
  }
  // Not delivered: forget the event ID so the agent's retry goes through.
  await env.DB.prepare("DELETE FROM events WHERE link_id = ? AND event_id = ?").bind(link.id, eventId).run();
  throw new HttpError(502, "apns_error", `APNs answered ${r.status || "nothing"}${r.reason ? ` (${r.reason})` : ""}`);
}

function log(msg: string, fields: Record<string, unknown>) {
  console.log(JSON.stringify({ msg, ...fields }));
}
