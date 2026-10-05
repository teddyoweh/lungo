// How a machine is doing: processor, memory and disk as three small meters, the mark the
// sidebar shows when something is nearly full, and the stop-when-idle setting.
import { useEffect, useState } from "react";
import { TriangleAlert } from "lucide-react";
import { api, errText, type Cost, type Health, type MachineView } from "../lib/api";
import { bytes, clock, minutesText, uptimeText, useCost, useHealth, useIdle } from "../lib/health";
import { runOp, toast } from "../lib/store";
import { cx } from "../lib/util";
import { Segmented, Spinner, useNow } from "./ui";

type Tone = "ok" | "warn" | "bad";
const fill: Record<Tone, string> = { ok: "bg-ok", warn: "bg-warn", bad: "bg-bad" };
const ink: Record<Tone, string> = { ok: "text-fg", warn: "text-warn", bad: "text-bad" };

/** Amber from 80%, red once it is nearly full (the point where the sidebar warns too). */
const fullness = (pct: number): Tone => (pct >= 90 ? "bad" : pct >= 80 ? "warn" : "ok");

interface Figure {
  label: string;
  pct: number;
  tone: Tone;
  detail: string; // the numbers behind the share
}

/** The three figures of a machine. */
function figures(h: Health): Figure[] {
  // A Mac fills its memory on purpose; how hard it has to work for room is what tells.
  const mem: Tone = h.os === "darwin" ? ((h.pressure ?? 1) >= 4 ? "bad" : (h.pressure ?? 1) >= 2 ? "warn" : "ok") : fullness(h.memPct);
  return [
    // A busy processor is work being done, not a problem: it never goes red.
    { label: "CPU", pct: h.cpuPct, tone: h.cpuPct >= 90 ? "warn" : "ok", detail: `load ${h.load[0].toFixed(2)} · ${h.cpus} cores` },
    { label: "Memory", pct: h.memPct, tone: mem, detail: `${bytes(h.memUsed)} of ${bytes(h.memTotal)}` },
    { label: "Disk", pct: h.disk.pct, tone: fullness(h.disk.pct), detail: `${bytes(h.disk.free)} free` },
  ];
}

function Bar({ pct, tone }: { pct: number; tone: Tone }) {
  return (
    <div className="h-[3px] overflow-hidden rounded-full bg-active">
      {/* a sliver stays visible however little is used */}
      <div className={cx("h-full rounded-full transition-[width] duration-500", fill[tone])} style={{ width: pct > 0 ? `max(4px, ${Math.min(100, pct)}%)` : 0 }} />
    </div>
  );
}

/** The meters on a machine's row: label and number on one line, the bar under it. */
export function HealthMeters({ name }: { name: string }) {
  const h = useHealth(name);
  if (!h) return null;
  if (h.error)
    return (
      <div className="truncate text-[11.5px] text-subtle" title={h.error}>
        No answer
      </div>
    );
  return (
    <div className="grid grid-cols-3 gap-x-3.5">
      {figures(h).map((f) => (
        <div key={f.label} className="min-w-0" title={`${f.label}: ${f.pct}% · ${f.detail}`}>
          <div className="flex items-baseline justify-between gap-1 leading-none whitespace-nowrap">
            <span className="text-[9px] font-medium tracking-[0.07em] text-subtle uppercase">{f.label === "Memory" ? "Mem" : f.label}</span>
            <span className={cx("text-[12px] tabular-nums", ink[f.tone])}>
              {f.pct}
              <span className="ml-px text-[9.5px] text-muted">%</span>
            </span>
          </div>
          <div className="mt-[7px]">
            <Bar pct={f.pct} tone={f.tone} />
          </div>
        </div>
      ))}
    </div>
  );
}

/** The meters in the machine sheet, with the numbers behind each share. */
export function HealthPanel({ name }: { name: string }) {
  const h = useHealth(name);
  if (!h) return null;
  if (h.error)
    return (
      <div className="selectable text-[12px] text-subtle" title={h.error}>
        The machine didn't answer the last health check.
      </div>
    );
  return (
    <div>
      <div className="grid grid-cols-3 gap-x-6">
        {figures(h).map((f) => (
          <div key={f.label} className="min-w-0">
            <div className="text-[9.5px] leading-none font-medium tracking-[0.07em] text-subtle uppercase">{f.label}</div>
            <div className="mt-2 flex items-baseline leading-none">
              <span className={cx("text-[19px] font-medium tracking-[-0.02em] tabular-nums", ink[f.tone])}>{f.pct}</span>
              <span className="ml-px text-[10.5px] text-muted">%</span>
            </div>
            <div className="mt-2">
              <Bar pct={f.pct} tone={f.tone} />
            </div>
            <div className="mt-1.5 truncate text-[11px] text-subtle" title={f.detail}>
              {f.detail}
            </div>
          </div>
        ))}
      </div>
      <div className="mt-3 text-[11.5px] text-subtle">
        Up {uptimeText(h.uptimeSec)}
        {h.root && (
          <>
            {" · system disk "}
            <span className={h.root.pct >= 80 ? ink[fullness(h.root.pct)] : undefined}>{h.root.pct}%</span>
            {`, ${bytes(h.root.free)} free`}
          </>
        )}
      </div>
      {h.warnings.length > 0 && (
        <div className="mt-2 flex flex-col gap-0.5 text-[11.5px] text-warn">
          {h.warnings.map((w) => (
            <div key={w} className="flex items-center gap-1.5">
              <TriangleAlert size={11} className="shrink-0" /> {w}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * The sidebar's mark: there only when a disk or the memory is nearly full; the tooltip says
 * which. It sits on the corner of the machine's icon (it comes right after the icon, pulled
 * back over it), so it takes no room from the name however narrow the row is.
 */
export function HealthMark({ name }: { name: string }) {
  const h = useHealth(name);
  if (!h || h.error || h.warnings.length === 0) return null;
  return (
    <span title={h.warnings.join("\n")} className="relative z-10 -mt-[11px] -mr-[3px] -ml-[14px] flex size-[11px] shrink-0 text-warn">
      <svg viewBox="0 0 12 12" width="11" height="11" aria-label="Nearly full">
        <path d="M6 1.2 11.2 10.4H0.8Z" fill="currentColor" stroke="var(--sidebar-solid)" strokeWidth="1.6" strokeLinejoin="round" paintOrder="stroke" />
        <path d="M6 4.6v2.6M6 8.7v.1" stroke="var(--sidebar-solid)" strokeWidth="1.3" strokeLinecap="round" fill="none" />
      </svg>
    </span>
  );
}

/** Why a stopped machine is stopped, when it stopped itself: "Stopped after 2h idle at 03:10". */
export function IdleNote({ name, className }: { name: string; className?: string }) {
  const idle = useIdle(name);
  if (!idle?.note) return null;
  return <span className={className}>{idle.note}</span>;
}

/** Stop when idle: the choice, and what the machine is doing about it right now. */
export function IdleControl({ mv }: { mv: MachineView }) {
  const m = mv.machine;
  const idle = useIdle(m.name);
  const h = useHealth(m.name);
  const now = useNow(30000);
  const [pending, setPending] = useState<number | null>(null);
  useEffect(() => setPending(null), [idle?.minutes]);
  if (!idle || m.provider === "ssh") return null; // never for a machine you added yourself
  if (!idle.offered)
    return (
      <div>
        <div className="text-[12px] font-medium text-muted">Stop when idle</div>
        <div className="mt-1 text-[11.5px] leading-snug text-subtle">{idle.why}</div>
      </div>
    );
  const minutes = pending ?? idle.minutes;
  // A limit set from the CLI that isn't one of the usual ones is offered too, while it is set.
  const choices = [0, ...new Set([...(idle.minutes ? [idle.minutes] : []), 60, 120, 240, 480])].sort((a, b) => a - b);
  const set = async (v: string) => {
    const next = parseInt(v);
    if (next === minutes) return;
    setPending(next);
    try {
      await runOp(api.setIdle(m.name, next), true);
    } catch (e) {
      setPending(null);
      toast("error", "Couldn't change it", errText(e));
    }
  };
  const w = h?.idle;
  let state: React.ReactNode = null;
  if (minutes > 0 && m.status === "stopped") state = idle.note ? `${idle.note}. Opening a session starts it.` : "Opening a session starts it.";
  else if (minutes > 0 && w?.at && w.limit === minutes) {
    if (w.active) state = `Busy now: ${w.reason}.`;
    else if (w.since) {
      const stops = new Date(w.since).getTime() + w.limit * 60000;
      const idleMin = Math.max(0, Math.floor((now - new Date(w.since).getTime()) / 60000));
      state = stops > now ? `Idle for ${minutesText(idleMin)} · stops at ${clock(new Date(stops).toISOString())} if nothing happens.` : "Idle past the limit: stopping.";
    }
  }
  return (
    <div>
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2 text-[12px] font-medium text-muted">
          Stop when idle
          {pending !== null && <Spinner size={11} />}
        </div>
        <Segmented value={String(minutes)} onChange={set} options={choices.map((c) => ({ value: String(c), label: c ? minutesText(c) : "Off" }))} />
      </div>
      <div className="mt-1.5 text-[11.5px] leading-snug text-subtle">
        {minutes > 0
          ? `Shuts down after ${minutesText(minutes)} with no session open, Claude not working and no load. Only the volume is billed while it is stopped.`
          : "Off: it runs until you stop it."}
        {idle.dryRun && minutes > 0 && " Dry run: it only logs that it would stop."}
      </div>
      {state && <div className="mt-1 text-[11.5px] leading-snug text-muted">{state}</div>}
    </div>
  );
}

const usd = (n: number) => (n >= 100 ? `$${Math.round(n)}` : `$${n.toFixed(2)}`);

/** What it costs, as a row of the sheet's list. Nothing when the price of its size isn't known. */
export function CostRow({ name }: { name: string }) {
  const c: Cost | undefined = useCost(name);
  if (!c) return null;
  const parts = [`${usd(c.soFar)} so far`, `up ${c.upHours < 10 ? c.upHours.toFixed(1) : Math.round(c.upHours)} h`];
  if (c.basis) parts.push(`${c.basis} prices`);
  return (
    <>
      <dt className="text-subtle">Cost</dt>
      <dd className="min-w-0">
        <div className="selectable truncate text-fg">
          ${c.hourly.toFixed(3)}/h running · ≈ {usd(c.month)} this month
        </div>
        <div className="text-[11.5px] leading-snug text-subtle" title="The volume is included. The estimate assumes the machine keeps running as much as it has so far this month.">
          {parts.join(" · ")}
        </div>
      </dd>
    </>
  );
}
