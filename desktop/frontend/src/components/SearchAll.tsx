// Search everything (⇧⌘F): the words, on the screen and in the scrollback of every open
// session and in every past conversation, on every machine and on this computer.
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Search } from "lucide-react";
import { SEARCH_MIN, closeDialogs, liveIn, resume, runSearch, showInSession, useHistory, type SearchRow } from "../lib/history";
import { LOCAL, machineLabel, useStore } from "../lib/store";
import { ago, cx, isMac } from "../lib/util";
import { BrandIcon, SessionIcon } from "./Brand";
import { DialogFooter, DialogFrame, MachineMark, shortDir, useCloseOnElsewhere, useScrollToSelected } from "./History";
import { Kbd, Spinner, useNow } from "./ui";

const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** Text with every place the words show marked, in either case. */
function Marked({ text, q }: { text: string; q: string }) {
  const words = q.trim().replace(/\s+/g, " ");
  if (!words) return <>{text}</>;
  const parts = text.split(new RegExp(`(${escapeRe(words)})`, "ig"));
  return (
    <>
      {parts.map((p, i) =>
        i % 2 ? (
          <mark key={i} className="rounded-[3px] bg-[color-mix(in_srgb,var(--amber)_26%,transparent)] px-[1px] text-fg">
            {p}
          </mark>
        ) : (
          p
        ),
      )}
    </>
  );
}

const GROUPS = ["Open sessions", "Past conversations", "Started by scripts"];
const groupOf = (r: SearchRow) => (r.kind === "live" ? 0 : r.auto ? 2 : 1);

export function SearchDialog() {
  const open = useHistory((s) => s.dialog === "search");
  return open ? <SearchBody /> : null;
}

function SearchBody() {
  useCloseOnElsewhere();
  const search = useHistory((s) => s.search);
  const sessions = useStore((s) => s.sessions.sessions);
  const now = useNow(30000);
  const [q, setQ] = useState(search.q); // the last search is still there when the dialog comes back
  const [picked, setPicked] = useState<string | null>(null); // the row chosen with the keys; none: the first
  const input = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => input.current?.select(), []);
  // Search a moment after the typing stops; a search under way for other words is dropped.
  useEffect(() => {
    const t = window.setTimeout(() => runSearch(q), q.trim().length < SEARCH_MIN ? 0 : 220);
    return () => window.clearTimeout(t);
  }, [q]);
  useEffect(() => setPicked(null), [search.id]);

  const rows = search.rows;
  const at = picked ? rows.findIndex((r) => r.key === picked) : -1;
  const sel = at >= 0 ? at : 0;
  const current = rows[sel] as SearchRow | undefined;
  useScrollToSelected(listRef, current?.key ?? "");

  const go = (r: SearchRow, split: boolean) => {
    const where = split ? "row" : "group";
    if (r.kind === "live" && r.session) showInSession(r.machine, r.session, search.q, where);
    else if (r.id) resume({ machine: r.machine, id: r.id, dir: r.dir }, where);
  };

  const onKey = (e: React.KeyboardEvent) => {
    const move = (d: number) => {
      e.preventDefault();
      const to = rows[Math.max(0, Math.min(rows.length - 1, sel + d))];
      if (to) setPicked(to.key);
    };
    if (e.key === "ArrowDown") move(1);
    else if (e.key === "ArrowUp") move(-1);
    else if (e.key === "PageDown") move(6);
    else if (e.key === "PageUp") move(-6);
    else if (e.key === "Enter") {
      e.preventDefault();
      if (current) go(current, e.altKey);
    } else if (e.key === "Escape") {
      e.preventDefault();
      closeDialogs();
    }
  };

  const short = q.trim().length < SEARCH_MIN;
  const stale = !short && q.trim() !== search.q; // typed further: the rows are for the words before
  const busy = search.running || stale;
  const failed = Object.keys(search.errors);
  const local = search.progress[LOCAL];
  let status: ReactNode;
  if (short) status = q.trim() ? "Keep typing…" : isMac ? "⇧⌘F" : "Ctrl+Alt+F";
  else if (busy) {
    const where = search.pending.map(machineLabel).join(", ");
    const far = local && search.pending.includes(LOCAL) && local.total > 400 ? ` (${local.scanned.toLocaleString()} of ${local.total.toLocaleString()} conversations)` : "";
    status = (
      <>
        <Spinner size={11} />
        <span className="truncate">
          searching{where ? ` ${where}` : ""}
          {far}…
        </span>
      </>
    );
  } else {
    const found = `${rows.length} place${rows.length === 1 ? "" : "s"}`;
    if (failed.length > 0)
      status = (
        <span className="truncate" title={failed.map((m) => `${machineLabel(m)}: ${search.errors[m]}`).join("\n")}>
          {found} · <span className="text-warn">{failed.map(machineLabel).join(", ")} didn't answer</span>
        </span>
      );
    else if (search.partial.length > 0) status = <span className="truncate">{`${found} · the oldest conversations on ${search.partial.map(machineLabel).join(", ")} weren't reached`}</span>;
    else status = found;
  }

  let lastGroup = -1;
  return (
    <DialogFrame>
      <div className="flex shrink-0 items-center gap-2.5 border-b border-line px-4">
        <Search size={15} className="text-subtle" />
        <input
          ref={input}
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={onKey}
          placeholder="Search every session and conversation…"
          className="h-12 flex-1 bg-transparent text-[14px] text-fg outline-none placeholder:text-subtle"
          spellCheck={false}
        />
        <Kbd>esc</Kbd>
      </div>
      <div ref={listRef} className={cx("min-h-0 flex-1 overflow-y-auto p-1.5 transition-opacity", stale && "opacity-60")}>
        {rows.length === 0 && (
          <div className="px-6 py-10 text-center text-[12.5px] leading-relaxed text-subtle">
            {short ? (
              <>
                Looks at what is on screen and in the scrollback of every open session,
                <br />
                and at everything you and Claude said in past conversations, on every machine.
              </>
            ) : busy ? (
              "Searching…"
            ) : (
              <>
                Nothing found for “{search.q}”
              </>
            )}
          </div>
        )}
        {rows.map((r) => {
          const g = groupOf(r);
          const header = g !== lastGroup;
          lastGroup = g;
          const session = r.kind === "live" ? sessions.find((s) => s.machine === r.machine && s.name === r.session) : undefined;
          const live = r.kind === "past" ? liveIn(r.machine, r.id, sessions) : undefined;
          const title = (r.kind === "live" ? session?.title || r.title || r.session : r.title) || "(no title)";
          const hits = r.hits.filter((h) => h.role !== "title").slice(0, 2);
          return (
            <div key={r.key}>
              {header && <div className="px-2.5 pt-2 pb-1 text-[11px] font-medium text-subtle">{GROUPS[g]}</div>}
              <button
                data-row={r.key}
                onMouseMove={() => picked !== r.key && setPicked(r.key)}
                onClick={(e) => go(r, e.altKey)}
                className={cx("block w-full rounded-lg px-2.5 py-[5px] text-left", r === current && "bg-active")}
              >
                <div className="flex h-5 items-center gap-2.5 text-[12.5px] text-fg">
                  <span className="flex w-4 shrink-0 justify-center">
                    {r.kind === "live" ? <SessionIcon s={session} size={13} /> : <BrandIcon name="claude" size={13} className={cx(r.auto && "opacity-45 grayscale")} />}
                  </span>
                  <span className="min-w-0 flex-1 truncate">
                    <Marked text={title} q={search.q} />
                  </span>
                  {live && (
                    <span className="flex shrink-0 items-center gap-1 text-[10.5px] font-medium text-ok">
                      <span className="size-[5px] rounded-full bg-ok" />
                      open
                    </span>
                  )}
                  {r.kind === "live" && (r.matches ?? 0) > hits.length && <span className="shrink-0 text-[11px] text-subtle tabular-nums">{r.matches} lines</span>}
                  <span className="max-w-[190px] shrink-0 truncate text-[11.5px] text-subtle">{shortDir(r.dir, r.machine)}</span>
                  <span className="flex w-[98px] shrink-0 items-center justify-end gap-1.5 text-[11.5px] text-subtle">
                    <span className="truncate">{machineLabel(r.machine)}</span>
                    <MachineMark machine={r.machine} size={11} />
                  </span>
                  <span className="w-7 shrink-0 text-right text-[11.5px] text-subtle tabular-nums">{r.time ? ago(new Date(r.time).toISOString(), now) : ""}</span>
                </div>
                {hits.map((h, i) => (
                  <div key={i} className={cx("truncate pl-[26px] text-muted", r.kind === "live" ? "font-mono text-[11px] leading-[17px]" : "text-[11.5px] leading-[17px]")}>
                    {h.role === "user" && <span className="text-subtle">You: </span>}
                    {h.role === "assistant" && <span className="text-subtle">Claude: </span>}
                    <Marked text={h.snippet} q={search.q} />
                  </div>
                ))}
              </button>
            </div>
          );
        })}
      </div>
      <DialogFooter
        keys={[
          ["↵", !current ? "Open" : current.kind === "live" || liveIn(current.machine, current.id, sessions) ? "Go to session" : "Resume"],
          ["⌥↵", "In a split"],
        ]}
      >
        {status}
      </DialogFooter>
    </DialogFrame>
  );
}
