// The cloud button on a pane: the session goes to another device (the Mac mini, a cloud
// machine, this Mac) and carries on there, in the same pane. See lib/move.
import { Cloud, CloudUpload } from "lucide-react";
import { type Session } from "../lib/api";
import { canMove, deviceName, moveSession, useMoving, type Moving } from "../lib/move";
import { LOCAL, machineLabel, tabMachine, useStore, type Tab } from "../lib/store";
import { cx } from "../lib/util";
import { LocalIcon, ProviderIcon } from "./Brand";
import { showMenu, type MenuRow } from "./ContextMenu";

/** The device in a sentence: "this Mac", "mini". */
const deviceIn = (d: string) => (d === LOCAL ? "this Mac" : machineLabel(d));

/** The button, in a pane's title bar (or the top bar for a pane on its own); nothing when the pane can't move. */
export function MoveButton({ tab, session, className }: { tab: Tab; session?: Session; className?: string }) {
  const machines = useStore((s) => s.machines);
  const move = useMoving(tab.key);
  const from = tabMachine(tab);
  if (!from || !canMove(tab, session)) return null;
  const up = (status?: string) => status === "running" || !status;
  const devices = [
    ...machines
      .filter((m) => m.machine.name !== from)
      .sort((a, b) => Number(up(b.machine.status)) - Number(up(a.machine.status)) || a.machine.name.localeCompare(b.machine.name))
      .map((m) => ({
        id: m.machine.name,
        icon: <ProviderIcon provider={m.machine.provider} os={m.machine.os} size={13} />,
        hint: !up(m.machine.status) ? m.machine.status : m.machine.os === "darwin" ? "Mac" : m.machine.region || m.providerLabel,
        ok: up(m.machine.status),
      })),
    ...(from !== LOCAL ? [{ id: LOCAL, icon: <LocalIcon size={13} />, hint: "", ok: true }] : []),
  ];
  if (!devices.length) return null;

  const open = (e: React.MouseEvent<HTMLButtonElement>) => {
    e.stopPropagation();
    const r = e.currentTarget.getBoundingClientRect();
    const rows: MenuRow[] = [
      { custom: <div className="px-2 pt-1 pb-1 text-[11px] text-subtle">Move this session to</div> },
      ...devices.map((d) => ({ label: deviceName(d.id), icon: d.icon, hint: d.hint, disabled: !d.ok, onClick: () => void moveSession(tab, session, d.id) })),
    ];
    showMenu({ clientX: r.right, clientY: r.bottom + 4 }, rows);
  };
  return (
    <button
      title={move ? `Moving to ${deviceIn(move.to)}…` : `Move to another device: it carries on there (${devices.map((d) => deviceName(d.id)).join(", ")})`}
      disabled={!!move}
      onClick={open}
      onMouseDown={(e) => e.stopPropagation()} // not the start of dragging the pane
      className={cx("flex items-center justify-center rounded text-subtle transition-colors hover:bg-active hover:text-fg disabled:hover:bg-transparent", move && "text-accent", className ?? "size-5")}
    >
      {move ? <CloudUpload size={13} className="sky-lift" /> : <Cloud size={13} />}
    </button>
  );
}

/** Over the pane while its session is on its way: where to, and what is happening now. */
export function MoveOverlay({ move }: { move: Moving }) {
  const events = useStore((s) => (move.op ? s.ops[move.op]?.events : undefined));
  const last = events?.filter((e) => e.message).slice(-1)[0]?.message;
  const step = move.arriving ? `Starting on ${deviceIn(move.to)}` : (last ?? `Getting ${deviceIn(move.to)} ready`);
  return (
    <div className="z anim-fade pointer-events-none absolute inset-0 z-20 flex items-center justify-center px-6">
      <div className="flex max-w-full flex-col items-center gap-2 text-center">
        <div className="mb-1 flex size-10 items-center justify-center rounded-full bg-[color-mix(in_srgb,var(--accent)_13%,transparent)] text-accent">
          <CloudUpload size={18} className="sky-lift" />
        </div>
        <div className="text-[13px] font-medium text-fg">Moving to {deviceIn(move.to)}</div>
        <div className="max-w-[340px] truncate text-[11.5px] text-subtle tabular-nums">{step}</div>
        <div className="mt-1 h-[2px] w-[132px] overflow-hidden rounded-full bg-line">
          <div className="sky-sweep h-full w-2/5 rounded-full bg-accent" />
        </div>
      </div>
    </div>
  );
}
