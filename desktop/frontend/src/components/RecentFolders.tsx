// The folders you work in, on every device: where Claude ran lately, each with its git state,
// the sessions open in it and its conversations. One click sends a folder to another device
// as it is (files, git history, uncommitted work, and the Claude conversations), so Claude
// picks up there where it left off.
import { useEffect, useMemo, useState } from "react";
import { ArrowRight, Check, FolderOpen, GitBranch, Play, Search, SendHorizontal, SquareTerminal } from "lucide-react";
import { api, call, errText, type GitInfo } from "../lib/api";
import { resume } from "../lib/history";
import { LOCAL, machineLabel, openClaudeTab, openLocalTab, openShellTab, runOp, useStore, waitOp } from "../lib/store";
import { ago, cx, hasAgent, tildePath } from "../lib/util";
import { LocalIcon, ProviderIcon, SessionIcon } from "./Brand";
import { Button, GROUP, IconButton, Menu, Modal, Spinner, useNow } from "./ui";

interface FolderActivity {
  machine: string;
  dir: string;
  name: string;
  updated: string;
  conversations: number;
  latest: string;
  latestTitle: string;
  branch?: string;
  live?: string[];
}
interface RecentFoldersView {
  folders: FolderActivity[];
  errors: Record<string, string>;
}
interface MovePlan {
  from: string;
  to: string;
  fromDir: string;
  toDir: string;
  name: string;
  exists: boolean;
  conversations: number;
  git: boolean;
  skip: string[];
}
interface MoveResult extends MovePlan {
  bytes: number;
  latest: string;
}

const PER_LIST = 12; // a few rows of cards

/** A device as the sidebar names it. */
const deviceName = (d: string) => (d === LOCAL ? "This Mac" : machineLabel(d)); // machineLabel says "this computer"

let cache: RecentFoldersView | null = null; // shown at once when the page opens again

const DEVICE_KEY = "sky.recent.device";
const savedDevice = () => {
  try {
    return localStorage.getItem(DEVICE_KEY) || "all";
  } catch {
    return "all";
  }
};

export function RecentFolders({ fresh = false }: { fresh?: boolean }) {
  const machines = useStore((s) => s.machines);
  const [view, setView] = useState<RecentFoldersView | null>(cache);
  const [device, setDeviceState] = useState(savedDevice); // "all", or one device
  const [more, setMore] = useState(false);
  const [query, setQuery] = useState("");
  const [sending, setSending] = useState<{ f: FolderActivity; to: string } | null>(null);
  const now = useNow(30000);
  const setDevice = (d: string) => {
    setDeviceState(d);
    setMore(false);
    try {
      localStorage.setItem(DEVICE_KEY, d);
    } catch {
      /* storage unavailable */
    }
  };

  const load = () =>
    call<RecentFoldersView>("RecentFolders", fresh).then((v) => {
      cache = v;
      setView(v);
    });
  useEffect(() => {
    void load();
  }, []);

  // This computer first, then the machines in their usual order.
  const devices = useMemo(() => [LOCAL, ...machines.map((m) => m.machine.name)], [machines]);
  const up = useMemo(() => new Set([LOCAL, ...machines.filter((m) => m.machine.status !== "stopped" && m.machine.status !== "missing").map((m) => m.machine.name)]), [machines]);
  const count = (d: string) => view?.folders.filter((f) => f.machine === d).length ?? 0;
  const pick = device === "all" || devices.includes(device) ? device : "all";
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  const list = (view?.folders ?? []).filter(
    (f) => (pick === "all" || f.machine === pick) && words.every((w) => `${f.name} ${f.dir} ${f.latestTitle} ${f.branch ?? ""} ${deviceName(f.machine)}`.toLowerCase().includes(w)),
  );
  const shown = more ? list : list.slice(0, PER_LIST);
  const err = pick === "all" ? "" : view?.errors[pick];

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center gap-3">
        <div className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-lg bg-[color-mix(in_srgb,var(--fg)_5%,transparent)] px-2.5 text-subtle focus-within:bg-[color-mix(in_srgb,var(--fg)_8%,transparent)]">
          <Search size={13} className="shrink-0" />
          <input
            value={query}
            onChange={(e) => (setQuery(e.target.value), setMore(false))}
            onKeyDown={(e) => e.key === "Escape" && setQuery("")}
            placeholder={view ? `Find among ${view.folders.length} folders` : "Find a folder"}
            spellCheck={false}
            className="min-w-0 flex-1 bg-transparent text-[12.5px] text-fg outline-none placeholder:text-subtle"
          />
        </div>
        {/* which device: all of them in one list, or one */}
        <div className="flex shrink-0 items-center gap-1.5">
          {["all", ...devices].map((d) => (
            <button
              key={d}
              onClick={() => setDevice(d)}
              className={cx(
                "flex h-8 items-center gap-1.5 rounded-full border px-3 text-[12px] transition-colors",
                pick === d ? "border-transparent bg-fg text-bg" : "border-line text-muted hover:border-line-strong hover:text-fg",
              )}
            >
              {d !== "all" && <DeviceIcon name={d} />}
              {d === "all" ? "All" : deviceName(d)}
              {view && <span className={cx("text-[11px] tabular-nums", pick === d ? "opacity-60" : "text-subtle")}>{d === "all" ? view.folders.length : count(d)}</span>}
            </button>
          ))}
        </div>
      </div>
      {!view ? (
        <div className={cx(GROUP, "flex h-[120px] items-center justify-center")}>
          <Spinner size={15} className="text-subtle" />
        </div>
      ) : err ? (
        <div className={cx(GROUP, "px-4 py-5 text-[12.5px] text-subtle")}>Couldn't read {deviceName(pick)} right now.</div>
      ) : list.length === 0 ? (
        <div className={cx(GROUP, "px-4 py-5 text-[12.5px] text-subtle")}>{query ? "No folder matches." : "Nothing here yet."}</div>
      ) : (
        <>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(270px,1fr))] gap-3">
            {shown.map((f) => (
              <FolderCard
                key={f.machine + f.dir}
                f={f}
                now={now}
                showDevice={pick === "all"}
                targets={devices.filter((x) => x !== f.machine && up.has(x))}
                onSend={(to) => setSending({ f, to })}
              />
            ))}
          </div>
          {list.length > PER_LIST && (
            <button onClick={() => setMore((m) => !m)} className="self-center rounded-md px-3 py-1.5 text-[12px] text-subtle hover:bg-hover hover:text-fg">
              {more ? "Show fewer" : `Show ${list.length - PER_LIST} more`}
            </button>
          )}
        </>
      )}
      {sending && <SendDialog f={sending.f} to={sending.to} onClose={(sent) => (setSending(null), sent && void load())} />}
    </section>
  );
}

/** A folder's git state, asked for once it is on screen. */
function useGit(machine: string, dir: string): GitInfo | null {
  const [g, setG] = useState<GitInfo | null>(null);
  useEffect(() => {
    let off = false;
    api.paneGit(machine, dir).then((v) => !off && setG(v), () => {});
    return () => {
      off = true;
    };
  }, [machine, dir]);
  return g;
}

/**
 * One folder as a card: its name and the sessions open in it, where it is, what was last
 * done there (the latest conversation, or the last commit), and its branch with what has
 * changed. A click picks the work up: the latest conversation, or Claude in the folder.
 */
function FolderCard({ f, now, showDevice, targets, onSend }: { f: FolderActivity; now: number; showDevice: boolean; targets: string[]; onSend: (to: string) => void }) {
  const git = useGit(f.machine, f.dir);
  const home = useStore((s) => s.info?.home);
  const sessions = useStore((s) => s.sessions.sessions);
  const live = sessions.filter((s) => s.machine === f.machine && (s.path === f.dir || f.live?.includes(s.name)) && hasAgent(s));
  const path = tildePath(f.dir, f.machine === LOCAL ? home : undefined);
  const branch = git?.repo ? git.branch : f.branch;
  const open = () => {
    if (f.latest) resume({ machine: f.machine, id: f.latest, dir: f.dir });
    else if (f.machine === LOCAL) openLocalTab("group", f.dir, true);
    else openClaudeTab(f.machine, "group", f.dir);
  };
  const stop = (e: React.MouseEvent) => e.stopPropagation();
  return (
    <div
      onClick={open}
      title={f.latest ? `Pick up "${f.latestTitle || "the latest conversation"}"` : "Start Claude here"}
      className="group flex min-h-[132px] cursor-pointer flex-col rounded-xl border border-line px-4 pt-3.5 pb-3 transition-colors hover:border-line-strong hover:bg-[color-mix(in_srgb,var(--fg)_2.5%,transparent)]"
    >
      <div className="flex items-center gap-2">
        <FolderOpen size={14} className="shrink-0 text-subtle" />
        <span className="min-w-0 flex-1 truncate text-[13.5px] font-semibold text-fg">{f.name}</span>
        {live.length > 0 && (
          <span className="flex shrink-0 items-center gap-1" title={`${live.length} session${live.length === 1 ? "" : "s"} open here`}>
            {live.slice(0, 4).map((s) => (
              <SessionIcon key={s.name} s={s} size={12} />
            ))}
          </span>
        )}
      </div>
      <div className="mt-1 flex min-w-0 items-center gap-1.5 text-[11.5px] text-subtle">
        {showDevice && (
          <span className="flex shrink-0 items-center gap-1 text-muted">
            <DeviceIcon name={f.machine} />
            {deviceName(f.machine)}
            <span className="text-subtle">·</span>
          </span>
        )}
        <span className="min-w-0 truncate" title={f.dir}>
          {path}
        </span>
      </div>
      <div className="mt-2.5 line-clamp-2 min-h-[34px] text-[12px] leading-[17px] text-muted">{f.latestTitle || <span className="text-subtle">Nothing said here yet</span>}</div>
      <div className="mt-auto flex h-7 items-center gap-2 pt-2 text-[11.5px] text-subtle">
        <span className="flex min-w-0 flex-1 items-center gap-1">
          {branch && (
            <>
              <GitBranch size={11} className="shrink-0" />
              <span className="truncate">{branch}</span>
              {git?.changed ? <span className="shrink-0 text-warn">· {git.changed} changed</span> : null}
            </>
          )}
        </span>
        <span className="shrink-0 tabular-nums group-hover:hidden">
          {ago(f.updated, now)} · {f.conversations ? `${f.conversations} chat${f.conversations === 1 ? "" : "s"}` : "no chats"}
        </span>
        <span className="-mr-1.5 hidden shrink-0 items-center gap-0.5 group-hover:flex" onClick={stop}>
          {f.latest && (
            <IconButton label={`Resume "${f.latestTitle || "the latest conversation"}"`} className="size-7" onClick={open}>
              <Play size={13} />
            </IconButton>
          )}
          <IconButton label="Open a shell here" className="size-7" onClick={() => (f.machine === LOCAL ? openLocalTab("group", f.dir) : openShellTab(f.machine, "group", f.dir))}>
            <SquareTerminal size={13} />
          </IconButton>
          {targets.length > 0 && (
            <Menu
              align="right"
              trigger={
                <IconButton label="Send to another device, as it is" className="size-7">
                  <SendHorizontal size={13} />
                </IconButton>
              }
              items={targets.map((t) => ({ label: `Send to ${deviceName(t)}`, icon: <DeviceIcon name={t} />, onClick: () => onSend(t) }))}
            />
          )}
        </span>
      </div>
    </div>
  );
}

function DeviceIcon({ name }: { name: string }) {
  const m = useStore((s) => s.machines.find((x) => x.machine.name === name)?.machine);
  return m ? <ProviderIcon provider={m.provider} os={m.os} size={13} /> : <LocalIcon size={13} />;
}

/** What sending will do, then the send itself, then where to pick up. */
function SendDialog({ f, to, onClose }: { f: FolderActivity; to: string; onClose: (sent: boolean) => void }) {
  const [plan, setPlan] = useState<MovePlan | null>(null);
  const [error, setError] = useState("");
  const [op, setOp] = useState("");
  const [result, setResult] = useState<MoveResult | null>(null);
  const events = useStore((s) => (op ? s.ops[op]?.events : undefined));
  const sessions = useStore((s) => s.sessions.sessions);
  const working = sessions.filter((s) => s.machine === f.machine && s.path === f.dir && hasAgent(s) && s.state === "working").length;
  useEffect(() => {
    call<MovePlan>("PlanMove", f.machine, to, f.dir).then(setPlan, (e) => setError(errText(e)));
  }, [f.machine, to, f.dir]);

  const send = async () => {
    try {
      const id = await runOp(call<string>("MoveFolder", f.machine, to, f.dir), true);
      setOp(id);
      const end = await waitOp(id);
      if (end.error) setError(end.error);
      else setResult(end.result as MoveResult);
    } catch (e) {
      setError(errText(e));
    }
  };
  const last = events?.filter((e) => e.message).slice(-1)[0]?.message ?? "";
  const toLabel = deviceName(to);
  const pickUp = () => {
    if (!result) return;
    if (result.latest) resume({ machine: to, id: result.latest, dir: result.toDir });
    else if (to === LOCAL) openLocalTab("group", result.toDir, true);
    else openClaudeTab(to, "group", result.toDir);
    onClose(true);
  };

  return (
    <Modal open onClose={() => onClose(!!result)} width={520}>
      <div className="px-6 pt-6 pb-5">
        <div className="flex items-center gap-2.5 text-[15px] font-semibold text-fg">
          {result ? <Check size={16} className="text-ok" /> : <SendHorizontal size={15} className="text-accent" />}
          {result ? `${f.name} is on ${toLabel}` : `Send ${f.name} to ${toLabel}`}
        </div>
        {error ? (
          <div className="mt-4 rounded-lg bg-[color-mix(in_srgb,var(--red)_9%,transparent)] px-3 py-2.5 text-[12.5px] text-bad">{error}</div>
        ) : !plan ? (
          <div className="flex h-[140px] items-center justify-center">
            <Spinner size={15} className="text-subtle" />
          </div>
        ) : (
          <div className="mt-4 flex flex-col gap-3 text-[12.5px]">
            <div className={cx(GROUP, "px-4 py-3")}>
              <div className="flex items-center gap-2.5">
                <DeviceIcon name={f.machine} />
                <span className="w-[86px] shrink-0 text-muted">{deviceName(f.machine)}</span>
                <span className="min-w-0 truncate font-mono text-[11.5px] text-fg">{tildePath(plan.fromDir)}</span>
              </div>
              <div className="my-1.5 ml-[3px] flex h-3 items-center text-subtle">
                <ArrowRight size={11} className="rotate-90" />
              </div>
              <div className="flex items-center gap-2.5">
                <DeviceIcon name={to} />
                <span className="w-[86px] shrink-0 text-muted">{toLabel}</span>
                <span className="min-w-0 truncate font-mono text-[11.5px] text-fg">{tildePath(plan.toDir)}</span>
              </div>
            </div>
            <ul className="flex flex-col gap-1.5 text-muted">
              <li>
                <span className="text-fg">Every file</span>
                {plan.git ? ", with the git history, the branch and work not committed yet" : ""}. Left out: {plan.skip.slice(0, 4).join(", ")} and other caches, which each device builds for itself.
              </li>
              <li>
                {plan.conversations > 0 ? (
                  <>
                    <span className="text-fg">
                      {plan.conversations} Claude conversation{plan.conversations === 1 ? "" : "s"}
                    </span>{" "}
                    come along: pick any up there with Resume or <span className="font-mono text-[11.5px]">claude --resume</span>.
                  </>
                ) : (
                  "No Claude conversations ran here yet."
                )}
              </li>
              {plan.exists && <li className="text-warn">{toLabel} already has this folder: files with the same name are replaced, the others stay.</li>}
              {working > 0 && <li className="text-warn">Claude is working here right now: what it does from this moment on stays on {deviceName(f.machine)}.</li>}
              <li>The folder stays on {deviceName(f.machine)} too.</li>
            </ul>
            {op && !result && (
              <div className="flex items-center gap-2 text-subtle">
                <Spinner size={12} />
                <span className="truncate">{last || "Starting…"}</span>
              </div>
            )}
            {result && (
              <div className="text-muted">
                {result.bytes >= 1 << 20 ? `${(result.bytes / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(result.bytes / 1024))} KB`} sent
                {result.conversations > 0 && `, ${result.conversations} conversation${result.conversations === 1 ? "" : "s"} with it`}.
              </div>
            )}
          </div>
        )}
        <div className="mt-6 flex justify-end gap-2">
          {result ? (
            <>
              <Button variant="ghost" onClick={() => onClose(true)}>
                Done
              </Button>
              <Button variant="primary" icon={<Play size={13} />} onClick={pickUp}>
                {result.latest ? `Pick up on ${toLabel}` : `Open Claude on ${toLabel}`}
              </Button>
            </>
          ) : (
            <>
              <Button variant="ghost" onClick={() => onClose(false)}>
                {op ? "Hide" : "Cancel"}
              </Button>
              <Button variant="primary" icon={<SendHorizontal size={13} />} disabled={!plan || !!op || !!error} onClick={() => void send()}>
                Send
              </Button>
            </>
          )}
        </div>
      </div>
    </Modal>
  );
}
