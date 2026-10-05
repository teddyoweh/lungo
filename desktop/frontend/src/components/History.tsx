// Conversation history (⌘Y): every past Claude conversation, here and on every machine,
// newest first. Type to narrow, Enter to pick one up again where it ran.
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { GitBranch, History as HistoryIcon } from "lucide-react";
import { closeDialogs, folderOf, liveIn, loadHistory, resume, useHistory, type Conversation } from "../lib/history";
import { LOCAL, machineLabel, paneContext, useStore } from "../lib/store";
import { ago, cx, mod } from "../lib/util";
import { BrandIcon, LocalIcon, ProviderIcon } from "./Brand";
import { Kbd, Spinner, useNow } from "./ui";

// ---------- what both dialogs are made of ----------

/** The dialog's frame: the palette's, a little wider. */
export function DialogFrame({ children, width = 760 }: { children: ReactNode; width?: number }) {
  return (
    <div className="z anim-fade fixed inset-0 z-50 flex items-start justify-center bg-[var(--overlay)] px-4 pt-[10vh]" onMouseDown={(e) => e.target === e.currentTarget && closeDialogs()}>
      <div style={{ width }} className="anim-in flex max-h-[78vh] max-w-full flex-col overflow-hidden rounded-2xl border border-line-strong bg-raised shadow-pop">
        {children}
      </div>
    </div>
  );
}

/** The line under the list: what the keys do on the left, what is going on on the right. */
export function DialogFooter({ keys, children }: { keys: [string, string][]; children?: ReactNode }) {
  return (
    <div className="flex h-8 shrink-0 items-center gap-3.5 border-t border-line px-3.5 text-[11px] text-subtle">
      {keys.map(([k, what]) => (
        <span key={k} className="flex shrink-0 items-center gap-1.5">
          <Kbd>{k}</Kbd>
          {what}
        </span>
      ))}
      <span className="flex min-w-0 flex-1 items-center justify-end gap-1.5 truncate">{children}</span>
    </div>
  );
}

/** A machine's logo: its cloud, or the kind of computer it is. */
export function MachineMark({ machine, size = 12 }: { machine: string; size?: number }) {
  const machines = useStore((s) => s.machines);
  if (machine === LOCAL) return <LocalIcon size={size} className="text-muted" />;
  const m = machines.find((x) => x.machine.name === machine)?.machine;
  return <ProviderIcon provider={m?.provider} os={m?.os} size={size} className="text-muted" />;
}

/** A folder, short enough for a row: ~ for home, and only its last two names when it is deep. */
export function shortDir(dir: string | undefined, machine: string): string {
  if (!dir) return "";
  const t = folderOf(dir, machine);
  const parts = t.split("/");
  return parts.length > 4 ? `…/${parts.slice(-2).join("/")}` : t;
}

/** Closes the dialog when something else comes to the front: another dialog, a page, a pane. */
export function useCloseOnElsewhere() {
  const modal = useStore((s) => s.modal);
  const view = useStore((s) => s.view);
  const activeTab = useStore((s) => s.activeTab);
  const opened = useRef({ view, activeTab });
  useEffect(() => {
    if (modal || view !== opened.current.view || activeTab !== opened.current.activeTab) closeDialogs();
  }, [modal, view, activeTab]);
}

/** Keeps the selected row in view. */
export function useScrollToSelected(list: React.RefObject<HTMLDivElement | null>, selected: string | number) {
  useEffect(() => {
    list.current?.querySelector(`[data-row="${CSS.escape(String(selected))}"]`)?.scrollIntoView({ block: "nearest" });
  }, [list, selected]);
}

// ---------- history ----------

const DAY = 86400000;

/** The heading a conversation goes under, by when it was last active. */
function bucket(at: number, now: number): string {
  const d = new Date(now);
  d.setHours(0, 0, 0, 0);
  const today = d.getTime();
  if (at >= today) return "Today";
  if (at >= today - DAY) return "Yesterday";
  if (at >= today - 6 * DAY) return "This week";
  if (at >= today - 30 * DAY) return "This month";
  return "Older";
}

const MAX_ROWS = 300; // more than anyone scrolls through; typing narrows

function loadShowAuto(): boolean {
  try {
    return localStorage.getItem("sky.history.auto") === "1";
  } catch {
    return false;
  }
}

export function HistoryDialog() {
  const open = useHistory((s) => s.dialog === "history");
  return open ? <HistoryBody /> : null;
}

function HistoryBody() {
  useCloseOnElsewhere();
  const convs = useHistory((s) => s.convs);
  const reading = useHistory((s) => s.reading);
  const errors = useHistory((s) => s.errors);
  const sessions = useStore((s) => s.sessions.sessions);
  const now = useNow(30000);
  const [q, setQ] = useState("");
  const [scope, setScope] = useState("all"); // "all", "here", or a machine
  const [showAuto, setShowAuto] = useState(loadShowAuto);
  const [sel, setSel] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);

  // What was read last time shows at once; a fresh reading replaces it a moment later.
  useEffect(() => loadHistory(true), []);

  // "This folder": the project of the pane in front when the dialog opened. That is the
  // pane's own folder, or the nearest folder above it that has conversations (a pane deep in
  // a repository belongs to the repository). A home folder is nobody's project but its own.
  const pane = useMemo(() => {
    const c = paneContext();
    return c.dir ? { machine: c.machine ?? LOCAL, dir: c.dir.replace(/\/+$/, "") } : null;
  }, []);
  const here = useMemo(() => {
    if (!pane) return null;
    let dir = "";
    for (const c of convs) {
      if (c.machine !== pane.machine || c.dir.length <= dir.length) continue;
      if (c.dir === pane.dir || (pane.dir.startsWith(`${c.dir}/`) && !/^\/(Users|home)\/[^/]+$|^\/root$/.test(c.dir))) dir = c.dir;
    }
    return { machine: pane.machine, dir: dir || pane.dir };
  }, [convs, pane]);
  const isHere = (c: Conversation) => !!here && c.machine === here.machine && c.dir === here.dir;

  const typed = useMemo(() => (showAuto ? convs : convs.filter((c) => !c.auto)), [convs, showAuto]);
  const scopes = useMemo(() => {
    const machines = [...new Set(typed.map((c) => c.machine))].sort((a, b) => (a === LOCAL ? -1 : b === LOCAL ? 1 : a.localeCompare(b)));
    const list: { id: string; label: string; n: number }[] = [{ id: "all", label: "All", n: typed.length }];
    if (here) list.push({ id: "here", label: "This folder", n: typed.filter(isHere).length });
    for (const m of machines) list.push({ id: m, label: m === LOCAL ? "This computer" : m, n: typed.filter((c) => c.machine === m).length });
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [typed, here]);
  const autoCount = useMemo(() => convs.filter((c) => c.auto).length, [convs]);

  const shown = useMemo(() => {
    const terms = q.toLowerCase().split(/\s+/).filter(Boolean);
    const out: Conversation[] = [];
    for (const c of typed) {
      if (scope === "here" ? !isHere(c) : scope !== "all" && c.machine !== scope) continue;
      if (terms.length) {
        const hay = `${c.title}\n${c.dir}\n${machineLabel(c.machine)}\n${c.branch ?? ""}\n${c.id}`.toLowerCase();
        if (!terms.every((t) => hay.includes(t))) continue;
      }
      out.push(c);
    }
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [typed, scope, q, here]);
  const rows = shown.length > MAX_ROWS ? shown.slice(0, MAX_ROWS) : shown;

  useEffect(() => setSel(0), [q, scope, showAuto]);
  useEffect(() => {
    // A scope that emptied out (a machine went away) falls back to everything.
    if (scope !== "all" && !scopes.some((s) => s.id === scope)) setScope("all");
  }, [scope, scopes]);
  useScrollToSelected(listRef, sel);

  const current = rows[Math.min(sel, rows.length - 1)];
  const currentLive = current ? liveIn(current.machine, current.id, sessions) : undefined;

  const onKey = (e: React.KeyboardEvent) => {
    const move = (d: number) => {
      e.preventDefault();
      setSel((i) => Math.max(0, Math.min(rows.length - 1, i + d)));
    };
    if (e.key === "ArrowDown") move(1);
    else if (e.key === "ArrowUp") move(-1);
    else if (e.key === "PageDown") move(10);
    else if (e.key === "PageUp") move(-10);
    else if (e.key === "Enter") {
      e.preventDefault();
      if (current) resume(current, e.altKey ? "row" : "group");
    } else if (e.key === "Tab") {
      e.preventDefault();
      const i = scopes.findIndex((s) => s.id === scope);
      setScope(scopes[(i + (e.shiftKey ? -1 : 1) + scopes.length) % scopes.length].id);
    } else if (e.key === "Escape") {
      e.preventDefault();
      closeDialogs();
    }
  };

  const toggleAuto = () => {
    const v = !showAuto;
    setShowAuto(v);
    try {
      localStorage.setItem("sky.history.auto", v ? "1" : "0");
    } catch {
      /* storage unavailable */
    }
  };

  const failed = Object.keys(errors);
  let lastBucket = "";
  return (
    <DialogFrame>
      <div className="flex shrink-0 items-center gap-2.5 border-b border-line px-4">
        <HistoryIcon size={15} className="text-subtle" />
        <input
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={onKey}
          placeholder="Resume a conversation…"
          className="h-12 flex-1 bg-transparent text-[14px] text-fg outline-none placeholder:text-subtle"
          spellCheck={false}
        />
        <Kbd>esc</Kbd>
      </div>
      <div className="flex h-8 shrink-0 items-center gap-0.5 px-2.5 text-[11.5px]">
        {scopes.map((s) => (
          <button
            key={s.id}
            onMouseDown={(e) => e.preventDefault()} // the keyboard stays in the search field
            onClick={() => setScope(s.id)}
            className={cx("flex h-[22px] items-center gap-1.5 rounded-md px-2 transition-colors", scope === s.id ? "bg-active text-fg" : "text-subtle hover:text-fg")}
          >
            {s.id !== "all" && s.id !== "here" && <MachineMark machine={s.id} size={11} />}
            {s.label}
            <span className="tabular-nums opacity-60">{s.n}</span>
          </button>
        ))}
        <span className="flex-1" />
        {autoCount > 0 && (
          <button
            onMouseDown={(e) => e.preventDefault()}
            onClick={toggleAuto}
            title="Conversations started by scripts: the SDK, claude -p"
            className={cx("flex h-[22px] items-center gap-1.5 rounded-md px-2 transition-colors", showAuto ? "bg-active text-fg" : "text-subtle hover:text-fg")}
          >
            Started by scripts
            <span className="tabular-nums opacity-60">{autoCount}</span>
          </button>
        )}
      </div>
      <div ref={listRef} className={cx("min-h-0 flex-1 overflow-y-auto px-1.5 pb-1.5", q.trim() && "pt-0.5")}>
        {rows.length === 0 && (
          <div className="flex items-center justify-center gap-2 px-3 py-10 text-[12.5px] text-subtle">
            {reading.length > 0 && convs.length === 0 ? (
              <>
                <Spinner size={13} /> Reading conversations…
              </>
            ) : q.trim() ? (
              "No conversation matches"
            ) : scope === "here" ? (
              "No conversations in this folder yet"
            ) : (
              "No conversations yet"
            )}
          </div>
        )}
        {rows.map((c, i) => {
          const b = q.trim() ? "" : bucket(c.at, now);
          const header = b !== lastBucket;
          lastBucket = b;
          const live = liveIn(c.machine, c.id, sessions);
          const dir = shortDir(c.dir, c.machine);
          return (
            <div key={`${c.machine}/${c.id}`}>
              {header && b && <div className="px-2.5 pt-2 pb-1 text-[11px] font-medium text-subtle">{b}</div>}
              <button
                data-row={i}
                onMouseMove={() => setSel(i)}
                onClick={(e) => resume(c, e.altKey ? "row" : "group")}
                title={`${c.title}\n${c.where}${c.messages ? ` · ${c.messages} messages` : ""}\n${c.id}`}
                className={cx("flex h-8 w-full items-center gap-2.5 rounded-lg px-2.5 text-left text-[12.5px] text-fg", i === sel && "bg-active")}
              >
                <span className="flex w-4 shrink-0 justify-center">
                  <BrandIcon name="claude" size={13} className={cx(c.auto && "opacity-45 grayscale")} />
                </span>
                <span className="min-w-[38%] flex-1 truncate">{c.title}</span>
                {live ? (
                  <span className="flex shrink-0 items-center gap-1 text-[10.5px] font-medium text-ok">
                    <span className="size-[5px] rounded-full bg-ok" />
                    open
                  </span>
                ) : (
                  c.running && (
                    <span className="shrink-0 text-[10.5px] text-subtle" title="A claude process on that machine has it open, outside Lungo">
                      in use
                    </span>
                  )
                )}
                {c.branch && (
                  <span className="flex max-w-[120px] min-w-0 shrink items-center gap-1 text-[11px] text-subtle">
                    <GitBranch size={10} className="shrink-0" />
                    <span className="truncate">{c.branch}</span>
                  </span>
                )}
                <span className="max-w-[190px] min-w-0 shrink truncate text-[11.5px] text-subtle">{dir}</span>
                <span className="flex w-[98px] shrink-0 items-center justify-end gap-1.5 text-[11.5px] text-subtle">
                  <span className="truncate">{machineLabel(c.machine)}</span>
                  <MachineMark machine={c.machine} size={11} />
                </span>
                <span className="w-7 shrink-0 text-right text-[11.5px] text-subtle tabular-nums">{ago(c.updated, now)}</span>
              </button>
            </div>
          );
        })}
        {shown.length > rows.length && <div className="px-2.5 py-2 text-[11.5px] text-subtle">{shown.length - rows.length} more. Type to narrow them down.</div>}
      </div>
      <DialogFooter
        keys={[
          ["↵", currentLive ? "Go to session" : "Resume"],
          ["⌥↵", "In a split"],
          ["⇥", "Machine"],
        ]}
      >
        {reading.length > 0 ? (
          <>
            <Spinner size={11} />
            <span className="truncate">reading {reading.map(machineLabel).join(", ")}…</span>
          </>
        ) : failed.length > 0 ? (
          <span className="truncate text-warn" title={failed.map((m) => `${machineLabel(m)}: ${errors[m]}`).join("\n")}>
            {failed.map(machineLabel).join(", ")} didn't answer
          </span>
        ) : (
          <span className="tabular-nums">
            {shown.length} conversation{shown.length === 1 ? "" : "s"} · {mod}Y
          </span>
        )}
      </DialogFooter>
    </DialogFrame>
  );
}
