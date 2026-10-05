import { useEffect, useState } from "react";
import { AlertTriangle, ArrowRight, KeyRound, RefreshCw } from "lucide-react";
import { api, errText, type ClaudeStatus, type Credential } from "../lib/api";
import { runOp, setState, toast, useStore } from "../lib/store";
import { ago, cx } from "../lib/util";
import { Button, Card, Empty, Page, Toggle, useNow } from "../components/ui";
import { OpLog } from "../components/OpLog";
import { BrandIcon, MachineIcon, SyncItemIcon } from "../components/Brand";
import { ActiveClaudeLine } from "../components/ClaudeAccounts";

export function SyncView() {
  const machines = useStore((s) => s.machines);
  const items = useStore((s) => s.info?.syncItems ?? []);
  const results = useStore((s) => s.syncResults);
  const ops = useStore((s) => s.ops);
  const opOrder = useStore((s) => s.opOrder);
  const now = useNow();
  const [claude, setClaude] = useState<ClaudeStatus | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [creds, setCreds] = useState<Credential[]>([]);

  useEffect(() => {
    api.claude().then(setClaude).catch(() => {});
    api.credentials().then((c) => setCreds(c ?? [])).catch(() => {});
    api.lastSyncAll().then((r) => setState({ syncResults: r ?? {} })).catch(() => {});
  }, []);

  const latestSync = opOrder.find((id) => ops[id]?.info.kind === "sync");
  const running = latestSync && ops[latestSync]?.info.running;

  const toggle = (name: string, item: string, on: boolean) => {
    const m = machines.find((x) => x.machine.name === name)?.machine;
    if (!m) return;
    const next = on ? [...new Set([...m.sync, item])] : m.sync.filter((x) => x !== item);
    api.setSyncItems(name, next).catch((e) => toast("error", "Couldn't save", errText(e)));
  };

  return (
    <Page
      title="Sync"
      subtitle="This computer is the source of truth; nothing flows back."
      actions={
        <Button variant="primary" icon={<RefreshCw size={13} className={running ? "spin" : ""} />} disabled={machines.length === 0 || !!running} onClick={() => runOp(api.sync([]))}>
          Sync all
        </Button>
      }
    >
      <div className="mx-auto flex max-w-[1180px] flex-col gap-5 px-6 py-5">
        {claude && (
          <div
            className={cx(
              "flex items-center gap-3 rounded-xl border px-4 py-3",
              claude.signedIn && claude.hasToken ? "border-line" : "border-[color-mix(in_srgb,var(--amber)_40%,transparent)] bg-[color-mix(in_srgb,var(--amber)_6%,transparent)]",
            )}
          >
            {claude.signedIn && claude.hasToken ? <BrandIcon name="claude" size={18} title="Claude" /> : <AlertTriangle size={16} className="text-warn" />}
            <div className="min-w-0 flex-1 text-[12.5px]">
              {!claude.signedIn ? (
                <span className="text-fg">Claude Code on this computer isn't signed in, so machines can't be signed in either.</span>
              ) : (
                <ActiveClaudeLine />
              )}
            </div>
            <Button size="sm" variant="ghost" icon={<KeyRound size={12} />} onClick={() => setState({ view: "accounts" })}>
              Accounts <ArrowRight size={12} />
            </Button>
          </div>
        )}

        {machines.length === 0 ? (
          <Card>
            <Empty icon={<RefreshCw size={18} />} title="Nothing to sync to yet" body="Add a machine and your Claude Code setup, GitHub accounts, API keys and CLI logins will follow it there." />
          </Card>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(320px,1fr))] gap-3">
            {machines.map((mv) => {
              const m = mv.machine;
              const r = results[m.name];
              const stopped = m.status === "stopped" || m.status === "missing";
              const on = items.filter((it) => m.sync.includes(it.id)).length;
              const syncing = !!running && ops[latestSync!]?.info.machine === m.name;
              return (
                <div key={m.name} className={cx("flex flex-col rounded-xl border px-4 pt-4 pb-3", open === m.name ? "border-line-strong" : "border-line")}>
                  <div className="flex items-start gap-3">
                    <span className={cx("mt-[1px] flex size-[24px] shrink-0 items-center justify-center", stopped && "opacity-50")}>
                      <MachineIcon m={m} size={20} />
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-[14px] font-semibold text-fg">{m.name}</div>
                      {/* the last sync: a click shows what it changed */}
                      <button onClick={() => setOpen(open === m.name ? null : m.name)} className="mt-[2px] block max-w-full truncate text-left text-[11.5px] hover:underline" title="What the last sync changed">
                        {r ? (
                          <span className={r.errors.length ? "text-warn" : "text-subtle"}>
                            {r.skipped ? r.skipped : `Synced ${ago(r.at, now)} ago · ${r.changes.length} change${r.changes.length === 1 ? "" : "s"}`}
                            {!r.skipped && r.errors.length > 0 && `, ${r.errors.length} problem${r.errors.length === 1 ? "" : "s"}`}
                          </span>
                        ) : (
                          <span className="text-subtle">Never synced from here</span>
                        )}
                      </button>
                    </div>
                    <Button size="sm" variant="outline" icon={<RefreshCw size={12} className={syncing ? "spin" : ""} />} disabled={stopped || !!running} onClick={() => runOp(api.sync([m.name]))}>
                      Sync
                    </Button>
                  </div>
                  <div className="mt-4 mb-1 flex items-center justify-between text-[10.5px] font-medium tracking-[0.07em] text-subtle uppercase">
                    <span>What goes</span>
                    <span className="tracking-normal normal-case tabular-nums">
                      {on} of {items.length}
                    </span>
                  </div>
                  <div className="-mx-2 flex flex-col">
                    {items.map((it) => (
                      <label key={it.id} title={it.id === "credentials" ? creds.map((c) => c.label).join(", ") : it.description} className="flex h-[34px] cursor-pointer items-center gap-2.5 rounded-md px-2 hover:bg-hover">
                        <SyncItemIcon id={it.id} size={14} />
                        <span className="min-w-0 flex-1 truncate text-[12.5px] text-fg">{it.label}</span>
                        <Toggle checked={m.sync.includes(it.id)} onChange={(v) => toggle(m.name, it.id, v)} />
                      </label>
                    ))}
                  </div>
                </div>
              );
            })}
          </div>
        )}

        {open && results[open] && (
          <Card className="anim-in p-4">
            <div className="mb-2 text-[13px] font-semibold text-fg">Last sync of {open}</div>
            {results[open].errors.length > 0 && (
              <div className="selectable mb-3 rounded-lg bg-[color-mix(in_srgb,var(--amber)_9%,transparent)] px-3 py-2 text-[12px] text-warn">
                {results[open].errors.map((e, i) => (
                  <div key={i}>{e}</div>
                ))}
              </div>
            )}
            {results[open].changes.length === 0 ? (
              <div className="text-[12.5px] text-subtle">Already in sync, nothing changed.</div>
            ) : (
              <ul className="selectable flex flex-col gap-1 font-mono text-[11.5px] text-muted">
                {results[open].changes.map((c, i) => (
                  <li key={i}>{c}</li>
                ))}
              </ul>
            )}
          </Card>
        )}

        {latestSync && (
          <Card className="p-4">
            <div className="mb-3 flex items-center justify-between">
              <div className="text-[13px] font-semibold text-fg">{running ? "Syncing…" : "Latest run"}</div>
              <span className="text-[11.5px] text-subtle">{ops[latestSync]?.info.title}</span>
            </div>
            <OpLog id={latestSync} compact />
          </Card>
        )}
      </div>
    </Page>
  );
}
