import type { Session, Status } from "./api";

export function ago(iso?: string, now = Date.now()): string {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  if (!t || t < 0 || isNaN(t)) return "";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 10) return "now";
  if (s < 60) return `${s}s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h}h`;
  return `${Math.round(h / 24)}d`;
}

/**
 * When a session was last active, in milliseconds: for Claude the last time its state changed
 * (a prompt, a tool, the end of a turn), otherwise the last output. 0 when not known.
 */
export function sessionTime(s: { claude?: boolean; agent?: string; stateAt?: string; activity?: string }): number {
  const ms = (iso?: string) => {
    const t = iso ? new Date(iso).getTime() : 0;
    return t > 946684800000 ? t : 0; // an unset time arrives as year 1
  };
  return (hasAgent(s) && ms(s.stateAt)) || ms(s.activity);
}

/** "now", or "5m ago": how long since a time in milliseconds; empty when there is none. */
export function agoText(t: number, now = Date.now()): string {
  if (!t) return "";
  const a = ago(new Date(t).toISOString(), now);
  return a === "now" || a === "" ? a : `${a} ago`;
}

export function money(n: number): string {
  if (!n) return "—";
  return n >= 100 ? `$${Math.round(n)}` : `$${n.toFixed(2)}`;
}

export function statusTone(s?: Status): "green" | "amber" | "red" | "grey" | "blue" {
  switch (s) {
    case "running":
      return "green";
    case "starting":
    case "stopping":
    case "provisioning":
      return "blue";
    case "unreachable":
    case "missing":
      return "red";
    case "stopped":
      return "grey";
    default:
      return "grey";
  }
}

export function statusLabel(s?: Status): string {
  if (!s) return "unknown";
  return s;
}

export type SessionTone = "working" | "waiting" | "idle" | "plain";
/** Whether a coding agent (Claude, Codex, Grok, Mantis) runs in the session. */
export const hasAgent = (s?: { claude?: boolean; agent?: string }): boolean => !!s && (!!s.claude || !!s.agent);
/** Which agent runs in the session ("" for none). */
export const agentOf = (s?: { claude?: boolean; agent?: string }): string => (s ? s.agent || (s.claude ? "claude" : "") : "");
/** An agent's name as people say it. */
export const AGENT_NAMES: Record<string, string> = { claude: "Claude Code", codex: "Codex", grok: "Grok Build", mantis: "Mantis" };

export function sessionTone(s?: Session): SessionTone {
  if (!s || !hasAgent(s)) return "plain";
  if (s.state === "working") return "working";
  if (s.state === "waiting") return "waiting";
  return "idle";
}

export function sessionLabel(s?: Session): string {
  switch (sessionTone(s)) {
    case "working":
      return "Working";
    case "waiting":
      return "Needs you";
    case "idle":
      return "Idle";
    default:
      return s?.command || "shell";
  }
}

export function baseName(p: string): string {
  const t = p.replace(/\/+$/, "");
  const i = t.lastIndexOf("/");
  return i >= 0 ? t.slice(i + 1) || "/" : t;
}

/** True inside the app's own web view; false in a plain browser pointed at wails dev. */
export function isNativeWebview(): boolean {
  const w = window as unknown as { webkit?: { messageHandlers?: { external?: unknown } }; chrome?: { webview?: unknown } };
  return !!(w.webkit?.messageHandlers?.external || w.chrome?.webview);
}

export function tildePath(p: string, home?: string): string {
  if (!p) return p;
  if (home && p.startsWith(home)) return "~" + p.slice(home.length);
  return p.replace(/^\/home\/[^/]+/, "~").replace(/^\/Users\/[^/]+/, "~");
}

/** Subsequence fuzzy score; -1 when no match. Higher is better. */
export function fuzzy(query: string, text: string): number {
  const q = query.toLowerCase().trim();
  if (!q) return 0;
  const t = text.toLowerCase();
  const idx = t.indexOf(q);
  if (idx >= 0) return 1000 - idx * 2 - (t.length - q.length) * 0.1;
  let score = 0;
  let ti = 0;
  let streak = 0;
  for (const ch of q) {
    const found = t.indexOf(ch, ti);
    if (found < 0) return -1;
    streak = found === ti ? streak + 1 : 0;
    score += 10 + streak * 5 - (found - ti);
    ti = found + 1;
  }
  return score;
}

export const isMac = typeof navigator !== "undefined" && /Mac/i.test(navigator.platform || navigator.userAgent);
export const mod = isMac ? "⌘" : "Ctrl+Shift+";

export function cx(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(" ");
}

export const PROVIDER_SHORT: Record<string, string> = { gcp: "GCP", aws: "AWS", azure: "Azure", ssh: "SSH" };
