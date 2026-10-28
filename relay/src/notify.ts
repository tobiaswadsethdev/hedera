// Event kinds and the APNs payload built from them. Radix, not the agent,
// decides the category and interruption level.

import { clip } from "./util";

export const KINDS = {
  shell_exited: { category: "SHELL_EXITED", level: "time-sensitive" },
  claude_turn_finished: { category: "CLAUDE_TURN", level: "active" },
  claude_needs_input: { category: "CLAUDE_INPUT", level: "active" },
  claude_failed: { category: "CLAUDE_FAILED", level: "time-sensitive" },
  claude_ended: { category: "CLAUDE_ENDED", level: "active" },
  claude_ended_abnormally: { category: "CLAUDE_ENDED", level: "time-sensitive" },
  test: { category: "TEST", level: "active" },
} as const;

export type Kind = keyof typeof KINDS;

// What an encrypted notification says until the app's extension decrypts it.
export const GENERIC_BODY: Record<Kind, string> = {
  shell_exited: "A shell exited",
  claude_turn_finished: "Claude finished a turn",
  claude_needs_input: "Claude needs your input",
  claude_failed: "Claude failed",
  claude_ended: "Claude session ended",
  claude_ended_abnormally: "Claude session ended",
  test: "Test notification",
};

// Either plaintext (title, subtitle, body) or `encrypted`, which only the
// phone can read (protocol/spec.md section 7.4).
export type Notification = {
  kind: Kind;
  sessionId?: string;
  turn?: number;
} & ({ title: string; subtitle?: string; body: string; encrypted?: undefined } | { encrypted: string });

export function apnsPayload(n: Notification, link: { id: string; machineId: string; label: string }) {
  const { category, level } = KINDS[n.kind];
  let alert: Record<string, string>;
  if (n.encrypted !== undefined) {
    alert = { title: clip(link.label, 100) || "Hedera", body: GENERIC_BODY[n.kind] };
  } else {
    alert = { title: clip(n.title, 100), body: clip(n.body, 500) };
    if (n.subtitle) alert.subtitle = clip(n.subtitle, 100);
  }
  const hedera: Record<string, string | number> = { kind: n.kind, machine_id: link.machineId, link_id: link.id };
  if (n.sessionId) hedera.session_id = n.sessionId;
  if (n.turn !== undefined) hedera.turn = n.turn;
  const aps: Record<string, unknown> = {
    alert,
    sound: "default",
    category,
    "thread-id": n.sessionId ? `${link.machineId}:${n.sessionId}` : link.machineId,
    "interruption-level": level,
  };
  if (n.encrypted !== undefined) {
    // Lets the app's notification service extension replace the text.
    aps["mutable-content"] = 1;
    hedera.enc = n.encrypted;
  }
  return { aps, hedera };
}
