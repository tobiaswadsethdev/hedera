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

export interface Notification {
  kind: Kind;
  title: string;
  subtitle?: string;
  body: string;
  sessionId?: string;
  turn?: number;
}

export function apnsPayload(n: Notification, link: { id: string; machineId: string }) {
  const { category, level } = KINDS[n.kind];
  const alert: Record<string, string> = { title: clip(n.title, 100), body: clip(n.body, 500) };
  if (n.subtitle) alert.subtitle = clip(n.subtitle, 100);
  const hedera: Record<string, string | number> = { kind: n.kind, machine_id: link.machineId, link_id: link.id };
  if (n.sessionId) hedera.session_id = n.sessionId;
  if (n.turn !== undefined) hedera.turn = n.turn;
  return {
    aps: {
      alert,
      sound: "default",
      category,
      "thread-id": n.sessionId ? `${link.machineId}:${n.sessionId}` : link.machineId,
      "interruption-level": level,
    },
    hedera,
  };
}
