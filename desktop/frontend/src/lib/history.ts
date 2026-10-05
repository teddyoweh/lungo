// Past conversations and search across everything: the state the two dialogs (History.tsx,
// SearchAll.tsx) and the palette share. It lives in a small store of its own.
import { useEffect, useSyncExternalStore } from "react";
import { call, errText, on } from "./api";
import type { Dir } from "./panes";
import { LOCAL, focusPane, getState, machineLabel, openClaudeTab, openSessionTab, setState, tabMachine, toast, type Tab } from "./store";
import { terminals } from "./terminals";
import { tildePath } from "./util";

/** One past Claude Code conversation on a machine (engine.Conversation). */
export interface Conversation {
  machine: string;
  id: string;
  dir: string; // the folder it ran in, where it is resumed
  title: string;
  updated: string;
  at: number; // updated, in milliseconds
  where: string; // its machine and folder, for a hint: "box · ~/code/api"
  messages?: number;
  branch?: string;
  size?: number;
  auto?: boolean; // started by a script (the SDK, claude -p), not typed
  live?: string;
  running?: boolean; // a claude process on the machine has it open, in sky or not
}

/** One place a search found the words (engine.SearchHit). */
export interface SearchHit {
  kind: "live" | "past";
  machine: string;
  session?: string;
  id?: string;
  dir?: string;
  title?: string;
  claude?: boolean;
  role?: string; // user | assistant | title
  at: string;
  updated: string;
  snippet: string;
  before?: string;
  after?: string;
  matches?: number;
  auto?: boolean;
}

/** The hits of one session or one conversation: one row of the search dialog. */
export interface SearchRow {
  key: string;
  kind: "live" | "past";
  auto: boolean;
  machine: string;
  session?: string;
  id?: string;
  dir?: string;
  title?: string;
  claude?: boolean;
  time: number; // a session's last output, or the last thing said in a conversation
  matches?: number;
  hits: SearchHit[];
}

interface HistoryView {
  machine: string;
  conversations: Omit<Conversation, "at" | "where">[] | null;
  error?: string;
}

interface SearchEvent {
  id: number;
  machines?: string[];
  machine?: string;
  hits?: SearchHit[] | null;
  done?: boolean;
  error?: string;
  partial?: boolean;
  scanned?: number;
  total?: number;
  end?: boolean;
}

export interface SearchState {
  q: string;
  id: number;
  rows: SearchRow[];
  running: boolean;
  pending: string[]; // machines still looking
  errors: Record<string, string>; // machines that didn't answer
  partial: string[]; // machines that ran out of time before their oldest conversations
  progress: Record<string, { scanned: number; total: number }>;
}

interface HistoryState {
  dialog: null | "history" | "search";
  convs: Conversation[]; // every machine's, newest first
  reading: string[]; // machines being read right now
  errors: Record<string, string>;
  loadedAt: number;
  search: SearchState;
}

const noSearch = (q = ""): SearchState => ({ q, id: 0, rows: [], running: false, pending: [], errors: {}, partial: [], progress: {} });

let state: HistoryState = { dialog: null, convs: [], reading: [], errors: {}, loadedAt: 0, search: noSearch() };
const listeners = new Set<() => void>();
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};
function set(patch: Partial<HistoryState> | ((s: HistoryState) => Partial<HistoryState>)) {
  state = { ...state, ...(typeof patch === "function" ? patch(state) : patch) };
  listeners.forEach((l) => l());
}

/** Subscribe to a slice. Selectors must return existing references, not new objects. */
export function useHistory<T>(sel: (s: HistoryState) => T): T {
  return useSyncExternalStore(subscribe, () => sel(state));
}
export const historyState = () => state;

// ---------- the dialogs ----------

/** Opens a dialog, or closes it when it is the one open (the shortcut toggles). */
export function toggleDialog(which: "history" | "search") {
  if (state.dialog === which) return void closeDialogs();
  setState({ modal: null }); // one dialog at a time
  if (which === "history") loadHistory();
  set({ dialog: which });
}

/**
 * Closes whichever dialog is open; false when none was. The keyboard goes back to the pane in
 * front (a pane that is only just being opened takes it by itself).
 */
export function closeDialogs(): boolean {
  if (!state.dialog) return false;
  if (state.dialog === "search") stopSearch();
  set({ dialog: null });
  requestAnimationFrame(() => {
    const s = getState();
    if (s.view === "sessions" && !s.modal && !state.dialog && s.activeTab) terminals.get(s.activeTab)?.focus();
  });
  return true;
}

// ---------- history ----------

/** Where conversations can be: this computer, then every machine that isn't off. */
export function historyMachines(): string[] {
  return [LOCAL, ...getState().machines.filter((m) => m.machine.status !== "stopped" && m.machine.status !== "missing").map((m) => m.machine.name)];
}

const ms = (iso: string) => {
  const t = Date.parse(iso);
  return t > 0 ? t : 0;
};

/**
 * Reads every machine's conversations, each on its own so the quick ones show first. The
 * dialog asks for a fresh reading every time it opens (force); the palette, which opens far
 * more often, makes do with one from the last minute.
 */
export function loadHistory(force = false) {
  if (state.reading.length > 0 || Date.now() - state.loadedAt < (force ? 1500 : 60000)) return;
  const machines = historyMachines();
  set((s) => ({ reading: machines, loadedAt: Date.now(), convs: s.convs.filter((c) => machines.includes(c.machine)) }));
  for (const m of machines) {
    const took = (list: Conversation[], error?: string) =>
      set((s) => {
        const errors = { ...s.errors };
        delete errors[m];
        if (error) errors[m] = error;
        // A machine that didn't answer keeps what it said last time.
        const convs = error ? s.convs : [...s.convs.filter((c) => c.machine !== m), ...list].sort((a, b) => b.at - a.at);
        return { convs, errors, reading: s.reading.filter((x) => x !== m) };
      });
    call<HistoryView>("History", m)
      .then((v) => took((v.conversations ?? []).map((c) => ({ ...c, at: ms(c.updated), where: `${machineLabel(c.machine)} · ${folderOf(c.dir, c.machine)}` })), v.error))
      .catch((e) => took([], errText(e)));
  }
}

/** A folder on a machine with ~ for the home folder (this computer's own, or any user's there). */
export const folderOf = (dir: string, machine: string) => tildePath(dir, machine === LOCAL ? getState().info?.home : undefined);

/** The conversations, for the palette: read when it opens, unless they were a moment ago. */
export function usePastConversations(): Conversation[] {
  useEffect(() => loadHistory(), []);
  return useHistory((s) => s.convs);
}

/** The open pane a conversation is in, when there is one. */
function paneOf(machine: string, id: string): Tab | undefined {
  return getState().tabs.find((t) => t.sid === id && tabMachine(t) === machine && t.claude && !t.exited);
}

/**
 * The session a conversation is open in right now, when it is: then there is nothing to
 * resume, only somewhere to go. The session list says so (it is polled every few seconds).
 */
export function liveIn(machine: string, id?: string, sessions = getState().sessions.sessions): string | undefined {
  if (!id) return undefined;
  return sessions.find((s) => s.machine === machine && s.sid === id && s.claude && !!s.state)?.name ?? paneOf(machine, id)?.session;
}

/**
 * Picks a conversation up again: a new Claude pane on its machine, in its folder, with
 * `claude --resume`. One that is open already is gone to instead.
 */
export function resume(c: { machine: string; id: string; dir?: string }, where: "group" | Dir = "group") {
  closeDialogs();
  setState({ modal: null });
  const pane = paneOf(c.machine, c.id);
  if (pane) return focusPane(pane.key);
  const live = liveIn(c.machine, c.id);
  if (live) return openSessionTab(c.machine, live, where);
  if (c.machine === LOCAL && !getState().localTmux) {
    // Without tmux a local pane can't be told what to run: it would start a new conversation.
    toast("info", "Resuming on this computer needs tmux", "Install it from Settings, or run: claude --resume " + c.id);
    return;
  }
  openClaudeTab(c.machine, where, c.dir, undefined, c.id);
}

// ---------- search ----------

let searchSeq = Math.floor(Math.random() * 1e6) * 1000; // no search of an earlier page load has the same number
let wired = false;

function wire() {
  if (wired) return;
  wired = true;
  on<SearchEvent>("search", (ev) => {
    if (ev.id !== state.search.id) return; // an older search still reporting
    set((s) => {
      const cur = s.search;
      const next: SearchState = { ...cur };
      if (ev.machines) next.pending = ev.machines;
      if (ev.hits?.length) next.rows = addHits(cur.rows, ev.hits);
      if (ev.machine && ev.total) next.progress = { ...cur.progress, [ev.machine]: { scanned: ev.scanned ?? 0, total: ev.total } };
      if (ev.machine && ev.done) {
        next.pending = next.pending.filter((m) => m !== ev.machine);
        if (ev.error) next.errors = { ...cur.errors, [ev.machine]: ev.error };
        else if (ev.partial) next.partial = [...cur.partial, ev.machine];
      }
      if (ev.end) {
        next.running = false;
        next.pending = [];
      }
      return { search: next };
    });
  });
}

const rowKey = (h: SearchHit) => `${h.kind}\n${h.machine}\n${h.session ?? ""}\n${h.id ?? ""}`;

/** Adds hits to the rows: one row per session or conversation, open sessions first, each kind newest first. */
function addHits(rows: SearchRow[], hits: SearchHit[]): SearchRow[] {
  const out = [...rows];
  const at = new Map(out.map((r, i) => [r.key, i]));
  for (const h of hits) {
    const key = rowKey(h);
    const i = at.get(key);
    if (i !== undefined) {
      if (!out[i].hits.some((x) => x.snippet === h.snippet)) out[i] = { ...out[i], hits: [...out[i].hits, h] };
      continue;
    }
    at.set(key, out.length);
    out.push({
      key,
      kind: h.kind,
      auto: !!h.auto,
      machine: h.machine,
      session: h.session,
      id: h.id,
      dir: h.dir,
      title: h.title,
      claude: h.claude,
      time: ms(h.kind === "live" ? h.at : h.updated) || ms(h.at),
      matches: h.matches,
      hits: [h],
    });
  }
  const rank = (r: SearchRow) => (r.kind === "live" ? 0 : r.auto ? 2 : 1);
  return out.sort((a, b) => rank(a) - rank(b) || b.time - a.time || a.key.localeCompare(b.key));
}

/** The shortest query worth running (the backend refuses shorter ones too). */
export const SEARCH_MIN = 2;

/** Starts a search for q everywhere; the one before it is dropped. Results arrive as events. */
export function runSearch(q: string) {
  wire();
  q = q.trim();
  if (q.length < SEARCH_MIN) {
    if (state.search.running) stopSearch();
    set({ search: noSearch(q) });
    return;
  }
  const id = ++searchSeq;
  set({ search: { ...noSearch(q), id, running: true, pending: historyMachines() } });
  call<void>("SearchStart", id, q).catch((e) => {
    if (state.search.id === id) set((s) => ({ search: { ...s.search, running: false, pending: [], errors: { ...s.search.errors, [LOCAL]: errText(e) } } }));
  });
}

/** Stops the search that is running; what it found so far stays. */
export function stopSearch() {
  if (!state.search.running) return;
  set((s) => ({ search: { ...s.search, id: 0, running: false, pending: [] } }));
  call<void>("SearchStop").catch(() => {});
}

/** Goes to a session a search found the words in, scrolled to the latest place they show. */
export function showInSession(machine: string, session: string, q: string, where: "group" | Dir = "group") {
  closeDialogs();
  setState({ modal: null });
  openSessionTab(machine, session, where);
  // tmux scrolls the session itself (its copy mode), so it shows whenever the pane attaches.
  call<void>("SearchShow", machine, session, q).catch(() => {});
}
