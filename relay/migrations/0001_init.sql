-- Radix schema. Secrets are stored as SHA-256 hex digests only.

CREATE TABLE devices (
  id           TEXT PRIMARY KEY,                 -- d_…
  secret_hash  TEXT NOT NULL UNIQUE,
  apns_token   TEXT NOT NULL,
  environment  TEXT NOT NULL CHECK (environment IN ('sandbox', 'production')),
  created_at   INTEGER NOT NULL,                 -- unix ms
  updated_at   INTEGER NOT NULL,
  disabled_at  INTEGER                           -- APNs said the token is gone
);

CREATE TABLE links (
  id             TEXT PRIMARY KEY,               -- l_…
  device_id      TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  machine_id     TEXT NOT NULL,
  label          TEXT NOT NULL,
  token_hash     TEXT NOT NULL UNIQUE,
  created_at     INTEGER NOT NULL,
  last_event_at  INTEGER
);
CREATE INDEX links_device ON links(device_id);

-- Idempotency (24 h) and the per-link rate limit (1 min).
CREATE TABLE events (
  link_id     TEXT NOT NULL REFERENCES links(id) ON DELETE CASCADE,
  event_id    TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (link_id, event_id)
);
CREATE INDEX events_link_time ON events(link_id, created_at);

-- Device registrations per source address, for rate limiting.
CREATE TABLE registrations (
  ip  TEXT NOT NULL,
  at  INTEGER NOT NULL
);
CREATE INDEX registrations_ip_time ON registrations(ip, at);
