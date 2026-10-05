// The ⌃Tab / ⌥Tab switcher: every tab as a live miniature of what it shows, most recently
// used first, four to a row, while the modifier is held. Tab and the arrows move the frame,
// typing narrows the tabs down, letting go goes there.
import { useLayoutEffect, useMemo, useRef } from "react";
import { Columns2 } from "lucide-react";
import { groupOf, machineLabel, paneFolder, paneTitle, sessionFor, tabLastActive, tabMachine, switchCommit, switchSelect, useStore, visibleGroups, LOCAL, type Group, type Tab } from "../lib/store";
import { geometry, leaves } from "../lib/panes";
import { terminals } from "../lib/terminals";
import { drawTerminal } from "../lib/thumb";
import { agoText, cx, mod, sessionTime } from "../lib/util";
import { PaneIcon } from "./Panes";
import { Sparks } from "./ui";

const RATIO = 16 / 10;

/** One tab drawn small: each pane's terminal in its place in the split, kept live. */
function Preview({ group, width }: { group: Group; width: number }) {
  const ref = useRef<HTMLCanvasElement>(null);
  const height = Math.round(width / RATIO);
  // Drawn before the switcher is first painted, so no card ever shows up empty.
  useLayoutEffect(() => {
    const canvas = ref.current;
    if (!canvas) return;
    const dpr = Math.min(3, window.devicePixelRatio || 1);
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    const css = getComputedStyle(document.documentElement);
    const bg = css.getPropertyValue("--term-bg").trim() || "#000";
    const line = css.getPropertyValue("--line-strong").trim() || "#333";
    const draw = () => {
      const ctx = canvas.getContext("2d");
      if (!ctx) return;
      const W = canvas.width;
      const H = canvas.height;
      ctx.fillStyle = bg;
      ctx.fillRect(0, 0, W, H);
      const { rects, dividers } = geometry(group.layout);
      for (const key of leaves(group.layout)) {
        const r = rects[key];
        const term = terminals.get(key);
        // Rows at least 7.5 points high, so the text can be read.
        if (r && term) drawTerminal(ctx, term, r.x * W, r.y * H, r.w * W, r.h * H, 7.5 * dpr);
      }
      ctx.fillStyle = line;
      for (const d of dividers) {
        if (d.dir === "row") ctx.fillRect(Math.round(d.at * W), d.rect.y * H, Math.max(1, dpr), d.rect.h * H);
        else ctx.fillRect(d.rect.x * W, Math.round(d.at * H), d.rect.w * W, Math.max(1, dpr));
      }
    };
    draw();
    const t = window.setInterval(draw, 500); // live while the switcher is up
    return () => window.clearInterval(t);
  }, [group, width, height]);
  return <canvas ref={ref} style={{ width, height }} className="block" />;
}

export function Switcher() {
  const sw = useStore((s) => s.switcher);
  const tabs = useStore((s) => s.tabs);
  const groups = useStore((s) => s.groups);
  const workspace = useStore((s) => s.workspace);
  const sessions = useStore((s) => s.sessions.sessions);
  const items = useMemo(
    () =>
      (sw?.order ?? [])
        .map((key) => ({ key, group: groupOf(key), tab: tabs.find((t) => t.key === key) }))
        .filter((x): x is { key: string; group: Group; tab: Tab } => !!x.group && !!x.tab),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [sw?.order, tabs, groups],
  );
  // The tab bar's order, for the ⌘1–⌘9 each card shows.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const shown = useMemo(() => visibleGroups().map((g) => g.id), [groups, workspace]);
  if (!sw?.shown || items.length === 0) return null;

  const now = Date.now();
  const cols = Math.min(4, items.length);
  const gap = 20;
  const room = Math.min(window.innerWidth - 80, 1720);
  const width = Math.round(Math.max(180, Math.min(400, (room - gap * (cols - 1)) / cols)));

  return (
    <div className="pointer-events-none fixed inset-0 z-[70] flex flex-col items-center justify-center bg-black/60 backdrop-blur-md">
      {sw.query && (
        <div className="z mb-5 flex h-8 items-center gap-2 rounded-full border border-white/10 bg-black/60 px-3.5 text-[12.5px] text-white shadow-pop">
          <span className="text-white/50">Filter</span>
          <span className="font-medium">{sw.query}</span>
          <span className="text-white/45 tabular-nums">
            {items.length} of {sw.all.length}
          </span>
        </div>
      )}
      <div
        className="pointer-events-auto grid max-h-[84vh] overflow-y-auto p-3 [scrollbar-width:none]"
        style={{ gridTemplateColumns: `repeat(${cols}, ${width}px)`, gap: `${gap + 6}px ${gap}px` }}
      >
        {items.map(({ key, group, tab }, i) => {
          const selected = i === sw.index;
          const session = sessionFor(tab, sessions);
          const keys = leaves(group.layout);
          const panes = tabs.filter((t) => keys.includes(t.key));
          const waiting = panes.some((t) => sessionFor(t, sessions)?.state === "waiting");
          const working = !waiting && panes.some((t) => sessionFor(t, sessions)?.state === "working");
          const machine = machineLabel(tabMachine(tab) ?? LOCAL).replace("this computer", "This Mac");
          const folder = paneFolder(tab);
          // When the tab was last active: the latest its sessions did anything; for a pane
          // without a session, when you were last in it.
          const seen = tabLastActive(group);
          const active = Math.max(0, ...panes.map((t) => sessionFor(t, sessions)).map((x) => (x ? sessionTime(x) : 0)));
          const when = agoText(active || (seen === "now" ? now : seen), now);
          const pos = shown.indexOf(group.id);
          return (
            <button
              key={key}
              data-switch-card
              onMouseDown={(e) => e.preventDefault()} // the keyboard stays where it is until the switch
              onMouseMove={(e) => (e.movementX || e.movementY) && !selected && switchSelect(i)} // a real move, not the grid appearing under the pointer
              onClick={() => {
                switchSelect(i);
                switchCommit();
              }}
              className="flex flex-col gap-2.5 text-left outline-none"
              style={{ width }}
            >
              <span
                className={cx(
                  "relative block overflow-hidden rounded-xl transition-[transform,box-shadow,opacity] duration-100",
                  selected
                    ? "scale-[1.025] opacity-100 shadow-[0_0_0_2px_var(--accent),0_0_0_7px_color-mix(in_srgb,var(--accent)_20%,transparent),0_24px_60px_-16px_rgba(0,0,0,0.8)]"
                    : "opacity-75 shadow-[0_0_0_1px_rgba(255,255,255,0.09),0_16px_40px_-20px_rgba(0,0,0,0.8)]",
                )}
              >
                <Preview group={group} width={width} />
                {(waiting || working) && (
                  <span
                    className={cx(
                      "absolute top-2 right-2 flex items-center gap-1.5 rounded-full px-2 py-[3px] text-[10.5px] font-medium backdrop-blur",
                      waiting ? "bg-[color-mix(in_srgb,var(--amber)_22%,rgba(0,0,0,0.55))] text-warn" : "bg-black/50 text-ok",
                    )}
                  >
                    {waiting ? <span className="size-[6px] rounded-full bg-warn" /> : <Sparks size={13} />}
                    {waiting ? "needs you" : "working"}
                  </span>
                )}
                {keys.length > 1 && (
                  <span className="absolute top-2 left-2 flex items-center gap-1 rounded-md bg-black/55 px-1.5 py-[3px] text-[10.5px] text-white/80 tabular-nums backdrop-blur">
                    <Columns2 size={10} /> {keys.length}
                  </span>
                )}
              </span>
              <span className="flex flex-col gap-1 px-1">
                <span className="flex items-center gap-2">
                  <PaneIcon tab={tab} session={session} size={14} />
                  <span className={cx("min-w-0 flex-1 truncate text-[13px] leading-tight", selected ? "font-medium text-white" : "text-white/70")}>{paneTitle(tab)}</span>
                  {pos >= 0 && pos < 9 && mod.length === 1 && (
                    <span className="shrink-0 text-[11px] text-white/40 tabular-nums">
                      {mod}
                      {pos + 1}
                    </span>
                  )}
                </span>
                <span className="flex items-center gap-1.5 pl-[22px] text-[11px] leading-tight text-white/45">
                  <span className="min-w-0 flex-1 truncate">{[machine, folder].filter(Boolean).join(" · ")}</span>
                  {when && <span className="shrink-0 tabular-nums">{when}</span>}
                </span>
              </span>
            </button>
          );
        })}
      </div>
      <div className="z mt-6 flex items-center gap-4 text-[11px] text-white/40">
        <span>
          <b className="font-medium text-white/60">Tab</b> next · <b className="font-medium text-white/60">⇧Tab</b> back
        </span>
        <span>
          <b className="font-medium text-white/60">arrows</b> move
        </span>
        <span>
          <b className="font-medium text-white/60">type</b> to filter
        </span>
        <span>
          <b className="font-medium text-white/60">let go</b> to open
        </span>
      </div>
    </div>
  );
}
