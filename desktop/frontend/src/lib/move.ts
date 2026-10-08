// Moving a session to another device from its pane (the cloud button). The session stops where
// it is, its folder and Claude conversations go over (the backend's MoveSession), and the pane
// picks the conversation up there, in the same place: the same tab, the same split.
//
// What stays behind is what each device runs or builds for itself: programs that were running
// (servers, background commands) and dependency folders. A session that was in the middle of
// something is told so as its next message, and goes on; one at rest waits for you.
import { useSyncExternalStore } from "react";
import { api, call, type Session } from "./api";
import { getState, LOCAL, machineLabel, movePaneTo, runOp, tabMachine, updateTab, waitOp, type Tab } from "./store";

interface MoveResult {
  toDir: string; // where the folder is now, on the other device
}

/** A pane on its way: where to, the operation doing it, and whether it is attaching there. */
export interface Moving {
  to: string;
  op?: string;
  arriving?: boolean;
}

const moving = new Map<string, Moving>();
const subs = new Set<() => void>();
function set(key: string, m: Moving | null) {
  if (m) moving.set(key, m);
  else moving.delete(key);
  for (const f of subs) f();
}
const subscribe = (f: () => void) => {
  subs.add(f);
  return () => subs.delete(f);
};

/** The pane's move, while there is one. */
export const useMoving = (key: string) => useSyncExternalStore(subscribe, () => moving.get(key));

/** The pane let go of its session to move it: it doesn't attach until the session is there. */
export const holdsOff = (key: string) => {
  const m = moving.get(key);
  return !!m && !m.arriving;
};

/** The pane shows its session again, where it moved to. */
export function arrived(key: string) {
  if (moving.get(key)?.arriving) set(key, null);
}

/** A device as a person names it. */
export const deviceName = (d: string) => (d === LOCAL ? "This Mac" : machineLabel(d));

/** Whether a pane's session can move: Claude, in a folder, in a session that outlives the pane. */
export function canMove(tab: Tab, session?: Session): boolean {
  return !!tabMachine(tab) && !!tab.session && !!(tab.claude || session?.claude) && !!(tab.cwd ?? session?.path);
}

/** Sends the pane's session to another device, and goes on there. */
export async function moveSession(tab: Tab, session: Session | undefined, to: string) {
  const key = tab.key;
  const from = tabMachine(tab);
  const dir = tab.cwd ?? session?.path;
  if (!from || !dir || !tab.session || moving.has(key) || from === to) return;
  const sid = tab.sid ?? session?.sid;
  const ports = getState().ports[from]?.[tab.session]?.length ?? 0;
  const busy = session?.state === "working" || ports > 0;
  set(key, { to });
  // The pane lets go first: the session ending would otherwise read as the end of the pane.
  if (tab.termId) api.closeTerminal(tab.termId).catch(() => {});
  updateTab(key, { termId: undefined, url: undefined, retry: undefined });

  let op = "";
  let res: MoveResult | undefined;
  try {
    op = await runOp(call<string>("MoveSession", from, tab.session, to, dir), true);
    set(key, { to, op });
    const end = await waitOp(op);
    if (!end.error) res = end.result as MoveResult; // a failure says why in its own toast
  } catch {
    /* couldn't start: runOp said so */
  }
  if (!res) {
    // Still where it was. If it had stopped before the send failed, it is made again there
    // from its conversation; if not, the pane just attaches to it again.
    const stopped = !!op && getState().ops[op]?.events.some((e) => e.message.startsWith("Stopping it on"));
    set(key, null);
    if (stopped) movePaneTo(key, from, dir, sid);
    else updateTab(key, { retry: { n: 1, at: Date.now() } });
    return;
  }
  set(key, { to, op, arriving: true });
  const note = busy ? arrivalNote(from, to, res.toDir, ports > 0) : undefined;
  // Its own conversation, by its ID: the newest in the folder could be another pane's.
  movePaneTo(key, to, res.toDir, sid || undefined, note);
  window.setTimeout(() => arrived(key), 45_000); // in case it never shows: the pane says why itself
}

/** The next message for a session moved in the middle of its work. */
function arrivalNote(from: string, to: string, dir: string, servers: boolean): string {
  const was = from === LOCAL ? "this Mac" : from;
  const now = to === LOCAL ? "this Mac" : to;
  return [
    `(This session was just moved from ${was} to ${now}; the folder is now ${dir}.`,
    `Dependency folders such as node_modules and everything that was running${servers ? " (dev servers included)" : ""} stayed behind.`,
    `Reinstall and restart what you need, then carry on where you left off.)`,
  ].join(" ");
}
