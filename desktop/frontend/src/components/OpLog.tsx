import { useEffect, useMemo, useRef, useState } from "react";
import { AlertTriangle, Check, ChevronRight, CircleAlert, ExternalLink, Info } from "lucide-react";
import { api, type OpEvent } from "../lib/api";
import { setState, useStore } from "../lib/store";
import { cx } from "../lib/util";
import { Button, Modal, ModalHeader, Spinner } from "./ui";

interface Step {
  title: string;
  status: "running" | "done" | "failed";
  details: OpEvent[];
}

/** Groups an operation's events into steps with their details. */
function toSteps(events: OpEvent[], finished: boolean, failed: boolean): { steps: Step[]; logs: OpEvent[]; links: OpEvent[] } {
  const steps: Step[] = [];
  const logs: OpEvent[] = [];
  const links: OpEvent[] = [];
  for (const e of events) {
    if (e.level === "log") {
      logs.push(e);
      continue;
    }
    if (e.level === "link") links.push(e);
    if (e.level === "step") {
      const prev = steps[steps.length - 1];
      if (prev && prev.status === "running") prev.status = "done";
      steps.push({ title: e.message, status: "running", details: [] });
      continue;
    }
    if (e.level === "done") {
      const prev = steps[steps.length - 1];
      if (prev && prev.status === "running") {
        prev.status = "done";
        prev.details.push(e);
      } else steps.push({ title: e.message, status: "done", details: [] });
      continue;
    }
    if (steps.length === 0) steps.push({ title: "Working", status: "running", details: [] });
    steps[steps.length - 1].details.push(e);
  }
  if (finished) {
    const last = steps[steps.length - 1];
    if (last && last.status === "running") last.status = failed ? "failed" : "done";
  }
  return { steps, logs, links };
}

export function OpLog({ id, compact }: { id: string; compact?: boolean }) {
  const op = useStore((s) => s.ops[id]);
  const [showLogs, setShowLogs] = useState(false);
  const logRef = useRef<HTMLDivElement>(null);
  const { steps, logs, links } = useMemo(
    () => toSteps(op?.events ?? [], !!op?.end, !!op?.end?.error),
    [op?.events, op?.end],
  );
  useEffect(() => {
    if (showLogs && logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [logs.length, showLogs]);
  if (!op) return <div className="flex items-center gap-2 py-4 text-[12.5px] text-subtle"><Spinner /> Starting…</div>;
  const lastLink = links[links.length - 1];
  const tsPending = lastLink && op.info.running;
  return (
    <div className="flex flex-col gap-3">
      {tsPending && (
        <div className="anim-in flex items-center gap-3 rounded-xl border border-[color-mix(in_srgb,var(--accent)_40%,transparent)] bg-accent-soft px-3.5 py-3">
          <ExternalLink size={16} className="shrink-0 text-accent" />
          <div className="min-w-0 flex-1">
            <div className="text-[13px] font-medium text-fg">{lastLink.message}</div>
            <div className="truncate font-mono text-[11px] text-muted">{lastLink.url}</div>
          </div>
          <Button variant="primary" size="sm" onClick={() => api.openURL(lastLink.url!)}>
            Open
          </Button>
        </div>
      )}
      <ol className="flex flex-col">
        {steps.map((s, i) => (
          <li key={i} className="flex gap-3">
            <div className="flex flex-col items-center">
              <div
                className={cx(
                  "mt-0.5 flex size-[18px] shrink-0 items-center justify-center rounded-full",
                  s.status === "done" && "bg-[color-mix(in_srgb,var(--green)_16%,transparent)] text-ok",
                  s.status === "failed" && "bg-[color-mix(in_srgb,var(--red)_16%,transparent)] text-bad",
                  s.status === "running" && "text-accent",
                )}
              >
                {s.status === "done" ? <Check size={11} strokeWidth={3} /> : s.status === "failed" ? <CircleAlert size={12} /> : <Spinner size={14} className="text-accent" />}
              </div>
              {i < steps.length - 1 && <div className="my-0.5 w-px flex-1 bg-line" />}
            </div>
            <div className="min-w-0 flex-1 pb-3">
              <div className={cx("text-[13px]", s.status === "running" ? "font-medium text-fg" : "text-fg")}>{s.title}</div>
              {(!compact || s.status === "running") &&
                s.details.slice(-6).map((d, j) => (
                  <div
                    key={j}
                    className={cx(
                      "mt-0.5 flex items-start gap-1.5 text-[12px] leading-snug",
                      d.level === "warn" ? "text-warn" : d.level === "error" ? "text-bad" : "text-subtle",
                    )}
                  >
                    {d.level === "warn" ? <AlertTriangle size={11} className="mt-0.5 shrink-0" /> : d.level === "info" ? <Info size={11} className="mt-0.5 shrink-0 opacity-60" /> : null}
                    <span className="selectable min-w-0 break-words">{d.message}</span>
                  </div>
                ))}
            </div>
          </li>
        ))}
      </ol>
      {op.end?.error && (
        <div className="selectable rounded-lg bg-[color-mix(in_srgb,var(--red)_9%,transparent)] px-3 py-2.5 font-mono text-[11.5px] leading-relaxed whitespace-pre-wrap text-bad">
          {op.end.error}
        </div>
      )}
      {logs.length > 0 && (
        <div>
          <button onClick={() => setShowLogs((v) => !v)} className="flex items-center gap-1 text-[12px] text-subtle hover:text-fg">
            <ChevronRight size={12} className={cx("transition-transform", showLogs && "rotate-90")} />
            Machine output ({logs.length} lines)
          </button>
          {showLogs && (
            <div
              ref={logRef}
              className="selectable mt-2 max-h-56 overflow-y-auto rounded-lg border border-line bg-[var(--term-bg)] px-3 py-2 font-mono text-[11px] leading-[1.55] text-muted"
            >
              {logs.slice(-800).map((l, i) => (
                <div key={i} className="break-all whitespace-pre-wrap">
                  {l.message}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export function OpModal({ id, onClose }: { id: string; onClose: () => void }) {
  const op = useStore((s) => s.ops[id]);
  return (
    <Modal open onClose={onClose} width={560}>
      <ModalHeader
        title={op?.info.title ?? "Operation"}
        subtitle={op ? (op.info.running ? "Running…" : op.end?.error ? "Failed" : "Finished") : ""}
        onClose={onClose}
      />
      <div className="max-h-[60vh] overflow-y-auto px-5 py-4">
        <OpLog id={id} />
      </div>
      <div className="flex justify-end gap-2 border-t border-line px-5 py-3">
        {op?.info.running && (
          <Button variant="ghost" onClick={() => api.cancelOp(id)}>
            Cancel operation
          </Button>
        )}
        <Button onClick={() => setState({ modal: null })}>Close</Button>
      </div>
    </Modal>
  );
}
