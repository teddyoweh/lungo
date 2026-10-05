// Claude usage at the bottom of the sidebar: the account machines are on, how much of its
// 5-hour and weekly windows is used, and who is next. Click to manage accounts.
import type { ClaudeAccountView, UsageWindow } from "../lib/api";
import { setState } from "../lib/store";
import { cx } from "../lib/util";
import { useNow } from "./ui";
import { BrandIcon } from "./Brand";
import { useClaudeAccounts, when } from "./ClaudeAccounts";

/** A reset time short enough for the sidebar: "51m", "2h 10m", "Fri 6AM". */
const short = (iso: string | undefined, now: number) =>
  when(iso, now)
    .replace(/^in /, "")
    .replace(/:00/, "")
    .replace(/ (AM|PM)$/, "$1");

const limited = (v: ClaudeAccountView, now: number) => v.status.state === "limited" && !!v.status.resetsAt && new Date(v.status.resetsAt).getTime() > now;

const tone = (pct: number | null) => (pct === null ? "none" : pct >= 100 ? "bad" : pct >= 80 ? "warn" : "ok");
const fill = { none: "bg-active", ok: "bg-ok", warn: "bg-warn", bad: "bg-bad" };
const ink = { none: "text-subtle", ok: "text-fg", warn: "text-warn", bad: "text-bad" };

/** One limit as a thin meter: what it is, how much is used; when it resets is on hover. */
function Meter({ label, w, now }: { label: string; w?: UsageWindow; now: number }) {
  const pct = w ? Math.round(w.utilization * 100) : null;
  const t = tone(pct);
  return (
    <div className="min-w-0" title={w?.resetsAt && pct ? `${label}: ${pct}% used, resets ${when(w.resetsAt, now)}` : undefined}>
      <div className="flex items-baseline justify-between gap-1 text-[10.5px] leading-none whitespace-nowrap">
        <span className="text-subtle">{label}</span>
        <span className={cx("tabular-nums", ink[t])}>{pct === null ? "–" : `${pct}%`}</span>
      </div>
      <div className="mt-[6px] h-[2px] overflow-hidden rounded-full bg-active">
        {/* a sliver stays visible however little is used */}
        <div className={cx("h-full rounded-full transition-[width] duration-500", fill[t])} style={{ width: pct ? `max(3px, ${Math.min(100, pct)}%)` : 0 }} />
      </div>
    </div>
  );
}

export function UsageFooter() {
  const accounts = useClaudeAccounts();
  const now = useNow(30000);
  if (!accounts) return null;
  const open = () => setState({ view: "accounts" });
  if (accounts.length === 0)
    return (
      <button onClick={open} className="no-drag flex w-full items-center gap-2 px-4 py-3 text-left text-[11.5px] text-subtle hover:text-fg">
        <BrandIcon name="claude" size={12} /> Add a Claude account
      </button>
    );
  const active = accounts.find((a) => a.active) ?? accounts[0];
  const others = accounts.filter((a) => a !== active && !a.account.disabled);
  const name = (v: ClaudeAccountView) => (v.account.label || v.account.email || "Claude").replace(/@.*$/, "");
  const next = others.find((v) => v.next && !limited(v, now)) ?? others.find((v) => !limited(v, now));
  const resting = others.filter((v) => limited(v, now));
  const nextWeek = next?.status.windows?.seven_day;
  return (
    <button onClick={open} title="Claude accounts and usage" className="group no-drag flex w-full flex-col px-4 pt-2.5 pb-3 text-left">
      <div className="flex items-center gap-2 text-[11.5px] leading-none">
        <BrandIcon name="claude" size={12} />
        <span className="min-w-0 flex-1 truncate font-medium text-muted group-hover:text-fg">{name(active)}</span>
        {active.plan && <span className="shrink-0 text-[10.5px] text-subtle">{active.plan}</span>}
      </div>
      {limited(active, now) ? (
        <div className="mt-2.5 text-[10.5px] leading-none text-warn">At its limit · back {short(active.status.resetsAt, now)}</div>
      ) : (
        <div className="mt-3 grid grid-cols-2 gap-x-4">
          <Meter label="5-hour" w={active.status.windows?.five_hour} now={now} />
          <Meter label="Week" w={active.status.windows?.seven_day} now={now} />
        </div>
      )}
      {(next || resting.length > 0) && (
        <div className="mt-3 flex items-center gap-1.5 text-[10.5px] leading-none text-subtle">
          {next && (
            <span className="flex min-w-0 items-center gap-1.5" title={`Next when this one is used up: ${name(next)}`}>
              <span className="shrink-0">Then</span>
              <span className="min-w-0 truncate text-muted">{name(next)}</span>
              {nextWeek && <span className="shrink-0 tabular-nums">{Math.round(nextWeek.utilization * 100)}%</span>}
            </span>
          )}
          {resting.length > 0 && (
            <span className="ml-auto shrink-0" title={resting.map((v) => `${name(v)}: back ${short(v.status.resetsAt, now)}`).join("\n")}>
              {resting.length} resting
            </span>
          )}
        </div>
      )}
    </button>
  );
}
