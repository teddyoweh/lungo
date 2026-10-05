import { useEffect, useMemo, useState } from "react";
import {
  Copy,
  ExternalLink,
  Files,
  HardDrive,
  Monitor,
  MoreHorizontal,
  Network,
  Play,
  Plug,
  Plus,
  Power,
  RefreshCw,
  RotateCcw,
  Scaling,
  Server,
  SquareTerminal,
  Trash2,
  X,
} from "lucide-react";
import { api, errText, type Catalog, type MachineView, type Port, type Session } from "../lib/api";
import { LOCAL, isIdleShell, openClaudeTab, openSessionTab, openShellTab, refreshMachines, runOp, setState, toast, useStore, waitOp } from "../lib/store";
import { openFiles } from "../lib/peek";
import { openScreen } from "../lib/screen";
import { ago, cx, hasAgent, money, PROVIDER_SHORT, statusTone } from "../lib/util";
import {
  Badge,
  Button,
  Checkbox,
  CopyField,
  Dot,
  Empty,
  Field,
  IconButton,
  Input,
  Menu,
  Modal,
  ModalHeader,
  Page,
  Segmented,
  Select,
  Sheet,
  Spinner,
  Toggle,
  useNow,
} from "../components/ui";
import { OpLog } from "../components/OpLog";
import { BrandIcon, ProviderIcon, SessionIcon } from "../components/Brand";
import { addCandidate, discoverMachines, type Candidate } from "../lib/discover";
import { startsOnDemand, uptimeText, useHealth, useHealthView } from "../lib/health";
import { CostRow, HealthMeters, HealthPanel, IdleControl, IdleNote } from "../components/MachineHealth";

export function MachinesView() {
  const machines = useStore((s) => s.machines);
  const loaded = useStore((s) => s.machinesLoaded);
  const sessions = useStore((s) => s.sessions.sessions);
  const [refreshing, setRefreshing] = useState(false);
  const total = machines.reduce((n, m) => n + (m.machine.status === "stopped" ? m.stoppedCost : m.monthly), 0);
  const running = machines.filter((m) => m.machine.status === "running" || (m.machine.provider === "ssh" && m.machine.status !== "stopped")).length;
  const live = sessions.filter((x) => x.machine !== LOCAL && !isIdleShell(x)).length;
  const summary = [
    `${running} of ${machines.length} running`,
    live ? `${live} session${live === 1 ? "" : "s"}` : "",
    total > 0 ? `≈ ${money(total)}/mo` : "",
  ].filter(Boolean).join(" · ");
  return (
    <Page
      title="Machines"
      subtitle={machines.length > 0 ? summary : undefined}
      actions={
        <>
          <IconButton
            label="Refresh status"
            onClick={async () => {
              setRefreshing(true);
              await refreshMachines();
              setRefreshing(false);
            }}
          >
            <RefreshCw size={14} className={refreshing ? "spin" : ""} />
          </IconButton>
          <Button variant="ghost" onClick={() => setState({ modal: { type: "add-machine" } })}>
            Add existing
          </Button>
          <Button variant="primary" icon={<Plus size={14} />} onClick={() => setState({ modal: { type: "new-machine" } })}>
            New machine
          </Button>
        </>
      }
    >
      <div className="mx-auto max-w-[1180px] px-6 py-5">
        {loaded && machines.length === 0 && (
          <Empty
            icon={<Server size={18} />}
            title="No machines yet"
            body="Create a VM on Google Cloud, AWS or Azure with a persistent volume for /home, or add a machine you already have by SSH."
            action={
              <div className="flex gap-2">
                <Button variant="primary" icon={<Plus size={14} />} onClick={() => setState({ modal: { type: "new-machine" } })}>
                  New machine
                </Button>
                <Button variant="outline" onClick={() => setState({ modal: { type: "add-machine" } })}>
                  Add existing
                </Button>
              </div>
            }
          />
        )}
        <div className="grid grid-cols-[repeat(auto-fill,minmax(340px,1fr))] gap-3">
          {machines.map((m) => (
            <MachineCard key={m.machine.name} mv={m} sessions={sessions.filter((x) => x.machine === m.machine.name && !isIdleShell(x))} />
          ))}
        </div>
      </div>
    </Page>
  );
}

/** Where a machine is and what it is, in one quiet line. */
function whereLine(mv: MachineView): string {
  const m = mv.machine;
  const cloud = m.provider !== "ssh";
  return [
    cloud ? (PROVIDER_SHORT[m.provider] ?? m.provider) : m.os === "darwin" ? "Mac" : m.os === "linux" ? "Linux" : "Your machine",
    m.zone || m.region,
    cloud && m.size ? `${m.size}${mv.cpus ? ` · ${mv.cpus} vCPU · ${mv.memoryGB} GB` : ""}` : "",
    cloud && m.diskGB ? `${m.diskGB} GB /home` : "",
    m.tailscaleName ? `tailnet ${m.tailscaleName.split(".")[0]}` : mv.address,
  ]
    .filter(Boolean)
    .join(" · ");
}

/**
 * One machine as a card: what it is and how it is doing (its meters), the sessions running
 * on it (a click goes to one), what it costs, and what you can do with it: a new Claude
 * session, its files, its screen when it is a Mac, a shell. A click anywhere else opens its
 * details.
 */
function MachineCard({ mv, sessions }: { mv: MachineView; sessions: Session[] }) {
  const m = mv.machine;
  const busy = useStore((s) => s.opOrder.some((id) => s.ops[id]?.info.running && s.ops[id]?.info.machine === m.name));
  const h = useHealth(m.name);
  const stopped = m.status === "stopped" || m.status === "missing";
  const cloud = m.provider !== "ssh";
  const asleep = stopped && !startsOnDemand(useHealthView(), m.name);
  const tone = statusTone(m.status as never);
  const needs = sessions.filter((x) => hasAgent(x) && x.state === "waiting").length;
  const shown = [...sessions].sort((a, b) => Number(b.state === "waiting") - Number(a.state === "waiting") || Number(b.state === "working") - Number(a.state === "working")).slice(0, 4);
  const stop = (e: React.MouseEvent) => e.stopPropagation();
  return (
    <div
      onClick={() => setState({ machineSheet: m.name })}
      className="group flex min-h-[236px] cursor-default flex-col rounded-xl border border-line px-4 pt-4 pb-3 transition-colors hover:border-line-strong hover:bg-[color-mix(in_srgb,var(--fg)_2.5%,transparent)]"
    >
      <div className="flex items-start gap-3">
        <span className={cx("mt-[1px] flex size-[24px] shrink-0 items-center justify-center", stopped && "opacity-50")}>
          <ProviderIcon provider={m.provider} os={m.os} size={22} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-[14px] font-semibold text-fg">{m.name}</span>
            <span className="flex shrink-0 items-center gap-1.5 text-[11.5px] text-subtle">
              {busy ? <Spinner size={11} /> : <Dot tone={tone} className="size-[6px]" />}
              <span className="capitalize">{m.status || "unknown"}</span>
            </span>
          </div>
          <div className="mt-[3px] truncate text-[11.5px] text-subtle" title={whereLine(mv)}>
            {whereLine(mv)}
          </div>
        </div>
        <span onClick={stop} className="-mt-1 -mr-1.5 shrink-0">
          <MachineMenu mv={mv} />
        </span>
      </div>

      {/* how it is doing */}
      <div className="mt-4">
        {stopped ? (
          <IdleNote name={m.name} className="block text-[11.5px] leading-snug text-subtle" />
        ) : h ? (
          <>
            <HealthMeters name={m.name} />
            {!h.error && <div className="mt-2 text-[11px] text-subtle">Up {uptimeText(h.uptimeSec)}</div>}
          </>
        ) : (
          <div className="text-[11.5px] text-subtle">
            {m.status === "unknown" || !m.status ? (cloud ? `Its state is unknown: is ${PROVIDER_SHORT[m.provider] ?? m.provider} signed in on the Accounts page?` : "No reading yet") : "Checking…"}
          </div>
        )}
      </div>

      {/* its sessions: a click goes to one */}
      <div className="mt-3 -ml-2 flex min-w-0 flex-col" onClick={stop}>
        {shown.map((x) => (
          <button
            key={x.name}
            title={`${x.title || x.name}\n${x.path}`}
            onClick={() => openSessionTab(m.name, x.name)}
            className="flex h-[26px] min-w-0 items-center gap-2 rounded-md px-2 text-left text-[12px] text-muted transition-colors hover:bg-active hover:text-fg"
          >
            <SessionIcon s={x} size={12} />
            <span className="min-w-0 flex-1 truncate">{x.title || x.name}</span>
            {hasAgent(x) && x.state === "waiting" && <span className="shrink-0 text-[11px] text-warn">needs you</span>}
          </button>
        ))}
        {sessions.length > shown.length && <span className="px-2 pt-0.5 text-[11.5px] text-subtle">+{sessions.length - shown.length} more</span>}
        {sessions.length === 0 && !stopped && <span className="px-2 text-[12px] text-subtle">No sessions running</span>}
      </div>

      <div className="mt-auto flex items-center gap-1 pt-3" onClick={stop}>
        <span className="min-w-0 flex-1 truncate text-[12px] tabular-nums">
          {needs > 0 ? (
            <span className="font-medium text-warn">
              {needs} need{needs === 1 ? "s" : ""} you
            </span>
          ) : cloud ? (
            <>
              <span className="text-fg">{money(stopped ? mv.stoppedCost : mv.monthly)}</span>
              <span className="text-subtle">{stopped ? " while stopped" : " a month"}</span>
            </>
          ) : null}
        </span>
        {stopped && cloud ? (
          <Button size="sm" variant="outline" icon={<Play size={12} />} onClick={() => runOp(api.startMachine(m.name))}>
            Start
          </Button>
        ) : (
          <>
            <IconButton label="New Claude session here" className="size-8" disabled={asleep} onClick={() => openClaudeTab(m.name)}>
              <BrandIcon name="claude" size={14} />
            </IconButton>
            <IconButton label="Files on it: what was made lately" className="size-8" disabled={stopped} onClick={() => openFiles({ machine: m.name, session: "", dir: "" })}>
              <Files size={15} />
            </IconButton>
            {m.os === "darwin" && (
              <IconButton label="See and use its screen" className="size-8" disabled={stopped} onClick={() => openScreen(m.name)}>
                <Monitor size={15} />
              </IconButton>
            )}
            <Button size="sm" variant="outline" icon={<SquareTerminal size={13} />} disabled={asleep} className="ml-1" onClick={() => openShellTab(m.name)}>
              Shell
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

export function StatusBadge({ status }: { status?: string }) {
  const tone = statusTone(status as never);
  return (
    <Badge tone={tone} className="capitalize">
      <Dot tone={tone} className="size-1.5" />
      {status || "unknown"}
    </Badge>
  );
}

function MachineMenu({ mv }: { mv: MachineView }) {
  const m = mv.machine;
  const cloud = m.provider !== "ssh";
  const stopped = m.status === "stopped";
  return (
    <Menu
      trigger={
        <IconButton label="More">
          <MoreHorizontal size={15} />
        </IconButton>
      }
      items={[
        { label: "Open in Terminal app", icon: <ExternalLink size={13} />, onClick: () => api.connect(m.name).catch((e) => toast("error", "Couldn't open", errText(e))) },
        { label: `Copy "${mv.sshShort}"`, icon: <Copy size={13} />, onClick: () => api.copy(mv.sshShort).then(() => toast("info", "Copied", mv.sshShort)) },
        { label: "Copy full ssh command", icon: <Copy size={13} />, onClick: () => api.copy(mv.sshFull).then(() => toast("info", "Copied", mv.sshFull)) },
        "sep",
        ...(cloud
          ? [
              stopped
                ? { label: "Start", icon: <Play size={13} />, onClick: () => runOp(api.startMachine(m.name)) }
                : { label: "Stop", icon: <Power size={13} />, onClick: () => runOp(api.stopMachine(m.name)) },
              { label: "Restart", icon: <RotateCcw size={13} />, disabled: stopped, onClick: () => runOp(api.restartMachine(m.name)) },
            ]
          : []),
        { label: "Sync now", icon: <RefreshCw size={13} />, disabled: stopped, onClick: () => runOp(api.sync([m.name])) },
        { label: "Details…", icon: <Server size={13} />, onClick: () => setState({ machineSheet: m.name }) },
        "sep",
        { label: cloud ? "Delete…" : "Remove…", icon: <Trash2 size={13} />, danger: true, onClick: () => setState({ machineSheet: m.name }) },
      ]}
    />
  );
}

// ---------- detail sheet ----------

export function MachineSheet({ name, onClose }: { name: string; onClose: () => void }) {
  const mv = useStore((s) => s.machines.find((x) => x.machine.name === name));
  const info = useStore((s) => s.info);
  const tunnels = useStore((s) => s.tunnels);
  const lastSync = useStore((s) => s.syncResults[name]);
  const now = useNow();
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [ports, setPorts] = useState<Port[] | null>(null);
  const [scanning, setScanning] = useState(false);
  const [size, setSize] = useState("");
  const [disk, setDisk] = useState(0);
  const [confirm, setConfirm] = useState("");
  const [keepDisk, setKeepDisk] = useState(false);
  const [section, setSection] = useState<"overview" | "ports" | "sync" | "manage">("overview");
  const health = useHealthView();

  const m = mv?.machine;
  const cloud = m && m.provider !== "ssh";
  useEffect(() => {
    if (m && cloud) api.catalog(m.provider).then(setCatalog).catch(() => {});
    if (m) {
      setSize(m.size ?? "");
      setDisk(m.diskGB ?? 0);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [m?.name, m?.provider]);

  if (!mv || !m) return null;
  const stopped = m.status === "stopped";
  // A machine that stops when idle is started by opening a session on it.
  const asleep = stopped && !startsOnDemand(health, m.name);
  const myTunnels = tunnels.filter((t) => t.machine === m.name);

  const scan = async () => {
    setScanning(true);
    try {
      setPorts(await api.ports(m.name));
    } catch (e) {
      toast("error", "Couldn't list ports", errText(e));
    } finally {
      setScanning(false);
    }
  };

  return (
    <Sheet open onClose={onClose} width={500}>
      <div className="drag flex items-start gap-3 px-5 pt-5 pb-3">
        <div className="flex size-10 shrink-0 items-center justify-center text-fg">
          <ProviderIcon provider={m.provider} os={m.os} size={24} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h2 className="truncate text-[15px] font-semibold text-fg">{m.name}</h2>
            <StatusBadge status={m.status} />
          </div>
          <div className="mt-0.5 truncate text-[12px] text-subtle">
            {mv.providerLabel}
            {m.zone || m.region ? ` · ${m.zone || m.region}` : ""}
            {cloud && mv.monthly ? ` · ${money(stopped ? mv.stoppedCost : mv.monthly)}/mo` : ""}
          </div>
        </div>
        <IconButton label="Close" onClick={onClose}>
          <X size={15} />
        </IconButton>
      </div>
      <div className="flex items-center gap-1.5 px-5 py-1.5">
        <Button size="sm" variant="primary" icon={<SquareTerminal size={13} />} disabled={asleep} onClick={() => { openShellTab(m.name); onClose(); }}>
          Open shell
        </Button>
        <Button size="sm" variant="outline" icon={<ExternalLink size={12} />} disabled={stopped} onClick={() => api.connect(m.name).catch((e) => toast("error", "Couldn't open", errText(e)))}>
          Terminal app
        </Button>
        <div className="flex-1" />
        {cloud &&
          (stopped ? (
            <Button size="sm" variant="outline" icon={<Play size={12} />} onClick={() => runOp(api.startMachine(m.name))}>
              Start
            </Button>
          ) : (
            <Button size="sm" variant="ghost" icon={<Power size={12} />} onClick={() => runOp(api.stopMachine(m.name))}>
              Stop
            </Button>
          ))}
      </div>
      <div className="px-5 pt-3">
        <Segmented
          value={section}
          onChange={setSection}
          options={[
            { value: "overview", label: "Overview" },
            { value: "ports", label: "Ports" },
            { value: "sync", label: "Sync" },
            { value: "manage", label: "Manage" },
          ]}
        />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
        {section === "overview" && (
          <div className="flex flex-col gap-5">
            {!stopped && <HealthPanel name={m.name} />}
            <IdleControl mv={mv} />
            <div className="flex flex-col gap-2">
              <div className="text-[12px] font-medium text-muted">Connect from any terminal</div>
              <CopyField value={mv.sshShort} />
              <CopyField value={mv.sshFull} />
            </div>
            <dl className="grid grid-cols-[120px_1fr] gap-x-4 gap-y-2.5 text-[12.5px]">
              {cloud && <Row k="Account" v={m.account} mono />}
              {cloud && <Row k="Size" v={`${m.size}${mv.cpus ? ` · ${mv.cpus} vCPU, ${mv.memoryGB} GB` : ""}`} />}
              {cloud && <Row k="Volume" v={`${m.diskGB} GB at /home${m.volumeId ? ` · ${m.volumeId}` : ""}`} />}
              {cloud && <CostRow name={m.name} />}
              {m.publicIp && <Row k="Public IP" v={m.publicIp} mono />}
              {m.host && <Row k="Host" v={`${m.host}${m.port && m.port !== 22 ? `:${m.port}` : ""}`} mono />}
              {m.sshAlias && <Row k="SSH alias" v={m.sshAlias} mono />}
              <dt className="text-subtle">Tailscale</dt>
              <dd className="flex items-center gap-2">
                {m.tailscaleIp ? (
                  <span className="selectable font-mono text-[12px] text-fg">
                    {m.tailscaleName || m.tailscaleIp} <span className="text-subtle">({m.tailscaleIp})</span>
                  </span>
                ) : (
                  <>
                    <span className="text-subtle">Not on your tailnet</span>
                    {m.os !== "darwin" && (
                      <Button size="xs" variant="outline" icon={<Network size={11} />} disabled={stopped} onClick={() => runOp(api.joinTailscale(m.name))}>
                        Join
                      </Button>
                    )}
                  </>
                )}
              </dd>
              <Row k="User" v={m.user} mono />
              {m.os && <Row k="OS" v={m.os} />}
              <Row k="Added" v={m.createdAt ? `${new Date(m.createdAt).toLocaleDateString()} (${ago(m.createdAt, now)} ago)` : "—"} />
            </dl>
          </div>
        )}

        {section === "ports" && (
          <div className="flex flex-col gap-4">
            <div className="flex items-center justify-between">
              <div className="text-[12.5px] text-muted">Dev servers and anything listening inside the machine. Opening one forwards it to localhost.</div>
              <Button size="sm" variant="outline" loading={scanning} disabled={stopped} icon={<RefreshCw size={12} />} onClick={scan}>
                Scan
              </Button>
            </div>
            {ports === null && !scanning && <div className="py-6 text-center text-[12.5px] text-subtle">Scan to see listening ports.</div>}
            {ports && ports.length === 0 && <div className="py-6 text-center text-[12.5px] text-subtle">Nothing is listening right now.</div>}
            {ports && ports.length > 0 && (
              <div className="overflow-hidden rounded-lg border border-line">
                {ports.map((p) => {
                  const t = myTunnels.find((x) => x.remotePort === p.port);
                  return (
                    <div key={p.port} className="flex items-center gap-3 border-b border-line px-3 py-2 last:border-b-0">
                      <span className="w-14 font-mono text-[12.5px] font-medium text-fg">{p.port}</span>
                      <span className="min-w-0 flex-1 truncate text-[12px] text-subtle">
                        {p.process || "?"} · {p.address}
                      </span>
                      {t ? (
                        <Button size="xs" variant="outline" icon={<ExternalLink size={11} />} onClick={() => api.openURL(t.url)}>
                          localhost:{t.localPort}
                        </Button>
                      ) : (
                        <Button
                          size="xs"
                          variant="outline"
                          icon={<Plug size={11} />}
                          onClick={() => api.openPort(m.name, p.port).catch((e) => toast("error", "Couldn't forward", errText(e)))}
                        >
                          Open
                        </Button>
                      )}
                    </div>
                  );
                })}
              </div>
            )}
            <ManualPort name={m.name} disabled={stopped} />
            {myTunnels.length > 0 && (
              <div>
                <div className="mb-1.5 text-[12px] font-medium text-muted">Open tunnels</div>
                <div className="overflow-hidden rounded-lg border border-line">
                  {myTunnels.map((t) => (
                    <div key={t.id} className="flex items-center gap-3 border-b border-line px-3 py-2 text-[12.5px] last:border-b-0">
                      <button className="font-mono text-accent hover:underline" onClick={() => api.openURL(t.url)}>
                        localhost:{t.localPort}
                      </button>
                      <span className="flex-1 text-subtle">→ {m.name}:{t.remotePort}</span>
                      <IconButton label="Close tunnel" className="size-6" onClick={() => api.closeTunnel(t.id)}>
                        <X size={12} />
                      </IconButton>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}

        {section === "sync" && (
          <div className="flex flex-col gap-4">
            <div className="text-[12.5px] text-muted">What this computer pushes to {m.name}. This computer is the source of truth.</div>
            <div className="overflow-hidden rounded-lg border border-line">
              {(info?.syncItems ?? []).map((it) => {
                const on = m.sync.includes(it.id);
                return (
                  <div key={it.id} className="flex items-center gap-3 border-b border-line px-3 py-2.5 last:border-b-0">
                    <div className="min-w-0 flex-1">
                      <div className="text-[12.5px] font-medium text-fg">{it.label}</div>
                      <div className="text-[11.5px] leading-snug text-subtle">{it.description}</div>
                    </div>
                    <Toggle
                      checked={on}
                      onChange={(v) => api.setSyncItems(m.name, v ? [...m.sync, it.id] : m.sync.filter((x) => x !== it.id)).catch((e) => toast("error", "Couldn't save", errText(e)))}
                    />
                  </div>
                );
              })}
            </div>
            <div className="flex items-center justify-between">
              <div className="text-[12px] text-subtle">
                {lastSync ? `Last sync ${ago(lastSync.at, now)} ago · ${lastSync.changes.length} change(s)${lastSync.errors.length ? ` · ${lastSync.errors.length} problem(s)` : ""}` : "Not synced from this app yet"}
              </div>
              <Button size="sm" variant="outline" icon={<RefreshCw size={12} />} disabled={stopped} onClick={() => runOp(api.sync([m.name]))}>
                Sync now
              </Button>
            </div>
            {lastSync?.errors?.length ? (
              <div className="selectable rounded-lg bg-[color-mix(in_srgb,var(--amber)_9%,transparent)] px-3 py-2 text-[12px] text-warn">
                {lastSync.errors.map((e, i) => (
                  <div key={i}>{e}</div>
                ))}
              </div>
            ) : null}
          </div>
        )}

        {section === "manage" && (
          <div className="flex flex-col gap-5">
            {cloud && catalog && (
              <Field label="Machine size" hint="The machine restarts if it's running. Sessions in tmux end; your files stay.">
                <div className="flex gap-2">
                  <Select
                    className="flex-1"
                    value={size}
                    onChange={setSize}
                    options={catalog.sizes.map((s) => ({ value: s.id, label: `${s.id} — ${s.cpus} vCPU, ${s.memoryGB} GB · ${money(s.monthly)}/mo` }))}
                  />
                  <Button variant="outline" icon={<Scaling size={13} />} disabled={!size || size === m.size} onClick={() => runOp(api.resizeMachine(m.name, size))}>
                    Resize
                  </Button>
                </div>
              </Field>
            )}
            {cloud && (
              <Field label="Volume size (GB)" hint={`Volumes only grow. Now ${m.diskGB} GB${catalog ? ` · ${money((disk || 0) * catalog.diskPerGB)}/mo at the new size` : ""}.`}>
                <div className="flex gap-2">
                  <Input type="number" min={m.diskGB} value={disk || ""} onChange={(e) => setDisk(parseInt(e.target.value) || 0)} className="w-32" />
                  <Button variant="outline" icon={<HardDrive size={13} />} disabled={!disk || disk <= (m.diskGB ?? 0)} onClick={() => runOp(api.growDisk(m.name, disk))}>
                    Grow volume
                  </Button>
                </div>
              </Field>
            )}
            {cloud && (
              <div className="flex gap-2">
                <Button variant="outline" icon={<RotateCcw size={13} />} disabled={stopped} onClick={() => runOp(api.restartMachine(m.name))}>
                  Restart
                </Button>
              </div>
            )}
            <div className="rounded-xl border border-[color-mix(in_srgb,var(--red)_35%,transparent)] p-4">
              <div className="text-[13px] font-semibold text-bad">{cloud ? "Delete machine" : "Remove machine"}</div>
              <div className="mt-1 text-[12px] leading-relaxed text-muted">
                {cloud
                  ? "Deletes the VM. Keep the volume to bring /home back later by creating a machine with the same name."
                  : "Forgets this machine in Lungo. Nothing on the machine is touched."}
              </div>
              {cloud && (
                <div className="mt-3">
                  <Checkbox checked={keepDisk} onChange={setKeepDisk} label={<span className="text-[12.5px]">Keep the volume ({m.diskGB} GB, billed while kept)</span>} />
                </div>
              )}
              <div className="mt-3 flex gap-2">
                <Input value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder={`Type ${m.name} to confirm`} className="flex-1" />
                <Button
                  variant="danger"
                  disabled={confirm !== m.name}
                  onClick={async () => {
                    const id = await runOp(api.deleteMachine(m.name, keepDisk));
                    onClose();
                    await waitOp(id);
                  }}
                >
                  {cloud ? "Delete" : "Remove"}
                </Button>
              </div>
            </div>
          </div>
        )}
      </div>
    </Sheet>
  );
}

function Row({ k, v, mono }: { k: string; v?: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-subtle">{k}</dt>
      <dd className={cx("selectable min-w-0 truncate text-fg", mono && "font-mono text-[12px]")}>{v || "—"}</dd>
    </>
  );
}

function ManualPort({ name, disabled }: { name: string; disabled?: boolean }) {
  const [port, setPort] = useState("");
  return (
    <div className="flex gap-2">
      <Input value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ""))} placeholder="Any port, e.g. 3000" className="flex-1" mono />
      <Button
        variant="outline"
        disabled={disabled || !port}
        icon={<Plug size={12} />}
        onClick={() =>
          api
            .openPort(name, parseInt(port))
            .then(() => setPort(""))
            .catch((e) => toast("error", "Couldn't forward", errText(e)))
        }
      >
        Forward
      </Button>
    </div>
  );
}

// ---------- add existing machine ----------

/** Machines sky can already see, each one click away. */
function FoundMachines({ onAdd }: { onAdd: (op: string) => void }) {
  const [found, setFound] = useState<Candidate[] | null>(null);
  useEffect(() => {
    discoverMachines().then((c) => setFound(c ?? [])).catch(() => setFound([]));
  }, []);
  if (found === null)
    return (
      <div className="flex items-center gap-2 text-[12px] text-subtle">
        <Spinner size={12} /> Looking at your ssh config and tailnet…
      </div>
    );
  if (found.length === 0) return null;
  return (
    <div>
      <div className="mb-1.5 text-[11px] font-semibold tracking-[0.06em] text-subtle uppercase">Found</div>
      <div className="overflow-hidden rounded-xl border border-line">
        {found.map((c) => (
          <div key={c.source + c.host + (c.alias ?? "")} className="flex items-center gap-3 border-b border-line px-3 py-2.5 last:border-b-0">
            <span className="flex size-8 shrink-0 items-center justify-center rounded-lg border border-line bg-panel text-fg">
              {c.os?.toLowerCase() === "macos" || /mac/i.test(c.host + (c.alias ?? "")) ? <BrandIcon name="apple" size={15} /> : c.os?.toLowerCase() === "linux" ? <BrandIcon name="linux" size={15} /> : <Server size={14} className="text-subtle" />}
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="truncate font-mono text-[12.5px] font-medium text-fg">{c.name}</span>
                <span className={cx("size-1.5 shrink-0 rounded-full", c.online ? "bg-ok" : "bg-subtle")} title={c.online ? "Online" : "Offline"} />
              </div>
              <div className="truncate text-[11.5px] text-subtle">
                {c.detail}
                {c.otherSync ? ` · synced by ${c.otherSync} today; Lungo takes over` : ""}
              </div>
            </div>
            <Button
              size="xs"
              variant={c.online ? "primary" : "outline"}
              disabled={!c.online}
              title={c.online ? "Authorizes Lungo's key, makes sure tmux is there, and syncs your setup" : "It isn't reachable right now"}
              onClick={async () => onAdd(await runOp(addCandidate(c, c.name, true), true))}
            >
              {c.online ? "Add" : "Offline"}
            </Button>
          </div>
        ))}
      </div>
      <div className="mt-3 text-[11px] font-semibold tracking-[0.06em] text-subtle uppercase">Or enter it</div>
    </div>
  );
}

export function AddMachineDialog({ onClose }: { onClose: () => void }) {
  const [mode, setMode] = useState<"address" | "alias">("address");
  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [user, setUser] = useState("");
  const [port, setPort] = useState("22");
  const [key, setKey] = useState("");
  const [alias, setAlias] = useState("");
  const [setup, setSetup] = useState(false);
  const [tailscale, setTailscale] = useState(false);
  const [op, setOp] = useState<string | null>(null);
  const opState = useStore((s) => (op ? s.ops[op] : undefined));
  const nameOk = /^[a-z]([a-z0-9-]{0,40}[a-z0-9])?$/.test(name);
  const ready = nameOk && (mode === "alias" ? !!alias : !!host && !!user);
  const suggested = useMemo(() => (mode === "alias" ? alias : host.split(".")[0]).toLowerCase().replace(/[^a-z0-9-]/g, "-").replace(/^-+|-+$/g, ""), [mode, alias, host]);

  const submit = async () => {
    const id = await runOp(
      api.addMachine({
        name,
        host: mode === "address" ? host.trim() : "",
        user: mode === "address" ? user.trim() : "",
        port: parseInt(port) || 22,
        keyPath: mode === "address" ? key.trim() : "",
        alias: mode === "alias" ? alias.trim() : "",
        setup,
        tailscale,
      }),
      true,
    );
    setOp(id);
  };

  return (
    <Modal open onClose={onClose} width={540} dismissable={!opState?.info.running}>
      <ModalHeader title="Add an existing machine" subtitle="Any Linux box or Mac you can SSH into: a home server, a Mac mini, a VPS." onClose={onClose} icon={<Server size={15} />} />
      {!op ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (ready) submit();
          }}
        >
          <div className="flex flex-col gap-4 px-5 py-4">
            <FoundMachines onAdd={(id) => setOp(id)} />
            <Segmented
              value={mode}
              onChange={setMode}
              options={[
                { value: "address", label: "Host and user" },
                { value: "alias", label: "Existing SSH alias" },
              ]}
            />
            {mode === "address" ? (
              <div className="grid grid-cols-[1fr_1fr_80px] gap-3">
                <Field label="Host">
                  <Input mono autoFocus value={host} onChange={(e) => setHost(e.target.value)} placeholder="192.168.1.20" />
                </Field>
                <Field label="User">
                  <Input mono value={user} onChange={(e) => setUser(e.target.value)} placeholder="ubuntu" />
                </Field>
                <Field label="Port">
                  <Input mono value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ""))} />
                </Field>
              </div>
            ) : (
              <Field label="Alias from ~/.ssh/config" hint="Lungo connects with `ssh <alias>` and leaves your config alone.">
                <Input mono autoFocus value={alias} onChange={(e) => setAlias(e.target.value)} placeholder="macmini" />
              </Field>
            )}
            {mode === "address" && (
              <Field label="SSH key" hint="Leave empty to use Lungo's key, falling back to your usual keys (Lungo's key is then added for you).">
                <div className="flex gap-2">
                  <Input mono value={key} onChange={(e) => setKey(e.target.value)} placeholder="~/.ssh/id_ed25519" className="flex-1" />
                  <Button type="button" variant="outline" onClick={() => api.pickFile("Choose an SSH private key").then((p) => p && setKey(p))}>
                    Browse
                  </Button>
                </div>
              </Field>
            )}
            <Field label="Name in Lungo" hint={nameOk || !name ? "Lowercase letters, digits and dashes. Becomes `ssh <name>`." : "Use lowercase letters, digits and dashes, starting with a letter."}>
              <Input mono value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder={suggested || "home-server"} onFocus={() => !name && suggested && setName(suggested)} />
            </Field>
            <div className="flex flex-col gap-2 rounded-lg border border-line px-3 py-2.5">
              <label className="flex items-center justify-between gap-3">
                <span>
                  <span className="block text-[12.5px] font-medium text-fg">Install the toolchain</span>
                  <span className="block text-[11.5px] text-subtle">Linux only: git, tmux, Node, Docker, gh, Claude Code. Needs passwordless sudo.</span>
                </span>
                <Toggle checked={setup} onChange={setSetup} />
              </label>
              <label className="flex items-center justify-between gap-3">
                <span>
                  <span className="block text-[12.5px] font-medium text-fg">Join my tailnet</span>
                  <span className="block text-[11.5px] text-subtle">Linux only. You'll approve it in the browser.</span>
                </span>
                <Toggle checked={tailscale} onChange={setTailscale} />
              </label>
            </div>
          </div>
          <div className="flex justify-end gap-2 border-t border-line px-5 py-3">
            <Button variant="ghost" type="button" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" disabled={!ready}>
              Add machine
            </Button>
          </div>
        </form>
      ) : (
        <>
          <div className="max-h-[56vh] overflow-y-auto px-5 py-4">
            <OpLog id={op} />
          </div>
          <div className="flex justify-end gap-2 border-t border-line px-5 py-3">
            {opState?.end?.error && (
              <Button variant="ghost" onClick={() => setOp(null)}>
                Back
              </Button>
            )}
            {opState?.end && !opState.end.error ? (
              <Button
                variant="primary"
                onClick={() => {
                  onClose();
                  openShellTab(name);
                }}
              >
                Open shell
              </Button>
            ) : (
              <Button onClick={onClose}>{opState?.info.running ? "Run in background" : "Close"}</Button>
            )}
          </div>
        </>
      )}
    </Modal>
  );
}
