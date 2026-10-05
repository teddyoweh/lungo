// Notices in the bottom-right corner. A session that finished or needs you is a card you
// click to open it; anything else says what happened and may offer one action. Each stays
// for a while (the thin line at its foot is the time left) and stays while the pointer is on it.
import { useEffect, useRef, useState } from "react";
import { AlertCircle, Check, CheckCircle2, Info, X } from "lucide-react";
import { LOCAL, dismissToast, machineLabel, useStore, type Toast } from "../lib/store";
import { cx } from "../lib/util";
import { AgentIcon } from "./Brand";

export function Toasts() {
  const toasts = useStore((s) => s.toasts);
  return (
    <div className="z pointer-events-none fixed right-4 bottom-4 z-[60] flex w-[340px] flex-col gap-2">
      {toasts.map((t) => (
        <Notice key={t.id} t={t} />
      ))}
    </div>
  );
}

/** The time left, which stops running while the pointer is on the notice. */
function useCountdown(t: Toast, paused: boolean) {
  const left = useRef(t.ms);
  const [shown, setShown] = useState(1); // share of the time left, for the line
  useEffect(() => {
    if (paused) return;
    let last = performance.now();
    let raf = 0;
    const tick = (now: number) => {
      left.current -= now - last;
      last = now;
      if (left.current <= 0) return dismissToast(t.id);
      setShown(left.current / t.ms);
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [paused, t.id, t.ms]);
  return shown;
}

const deviceName = (m: string) => (m === LOCAL ? "This Mac" : machineLabel(m));

function Notice({ t }: { t: Toast }) {
  const [hover, setHover] = useState(false);
  const left = useCountdown(t, hover);
  const s = t.session;
  const waiting = s?.state === "waiting";
  const act = () => {
    t.action?.run();
    dismissToast(t.id);
  };
  return (
    <div
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      onClick={s ? act : undefined}
      className={cx(
        "anim-in group pointer-events-auto relative overflow-hidden rounded-2xl bg-raised shadow-[0_0_0_1px_var(--line-strong),0_18px_40px_-14px_rgba(0,0,0,0.6)]",
        s && "cursor-pointer transition-colors hover:bg-[color-mix(in_srgb,var(--fg)_4%,var(--raised))]",
      )}
    >
      <div className="flex items-start gap-3 px-3.5 pt-3 pb-3.5">
        {s ? (
          // the session's mark, with what happened on its corner
          <span className="relative mt-[1px] flex size-8 shrink-0 items-center justify-center rounded-[10px] bg-[color-mix(in_srgb,var(--fg)_6%,transparent)]">
            <AgentIcon agent={s.agent || "claude"} size={16} />
            <span className={cx("absolute -right-1 -bottom-1 flex size-[15px] items-center justify-center rounded-full ring-2 ring-[var(--raised)]", waiting ? "bg-warn" : "bg-ok")}>
              {waiting ? <span className="text-[9px] leading-none font-bold text-black">!</span> : <Check size={9} strokeWidth={3.2} className="text-black" />}
            </span>
          </span>
        ) : (
          <span className="mt-[1px] flex size-8 shrink-0 items-center justify-center">
            {t.kind === "success" ? <CheckCircle2 size={17} className="text-ok" /> : t.kind === "error" ? <AlertCircle size={17} className="text-bad" /> : <Info size={17} className="text-subtle" />}
          </span>
        )}
        <div className="min-w-0 flex-1 pt-[1px]">
          <div className="flex items-baseline gap-2">
            <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-fg">{t.title}</span>
            {s && <span className="shrink-0 text-[11px] text-subtle">{deviceName(s.machine)}</span>}
          </div>
          {t.body && (
            <div className={cx("selectable mt-[3px] line-clamp-2 text-[12px] leading-[17px] break-words", waiting ? "text-warn" : "text-muted")}>
              {t.body}
            </div>
          )}
          {!s && t.action && (
            <button onClick={act} className="mt-2.5 h-7 rounded-lg bg-[color-mix(in_srgb,var(--fg)_8%,transparent)] px-2.5 text-[12px] font-medium text-fg hover:bg-[color-mix(in_srgb,var(--fg)_13%,transparent)]">
              {t.action.label}
            </button>
          )}
        </div>
        <button
          title="Dismiss"
          onClick={(e) => {
            e.stopPropagation();
            dismissToast(t.id);
          }}
          className="-mt-0.5 -mr-1 flex size-6 shrink-0 items-center justify-center rounded-md text-subtle opacity-0 transition-opacity group-hover:opacity-100 hover:bg-hover hover:text-fg"
        >
          <X size={13} />
        </button>
      </div>
      {/* the time left */}
      <div className="absolute inset-x-0 bottom-0 h-[2px]">
        <div className={cx("h-full", waiting ? "bg-warn/60" : s ? "bg-ok/50" : "bg-[color-mix(in_srgb,var(--fg)_20%,transparent)]")} style={{ width: `${left * 100}%` }} />
      </div>
    </div>
  );
}
