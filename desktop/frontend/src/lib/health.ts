// Machine health and stop-when-idle: the latest check from the backend, kept here rather
// than in the global store. The backend sends a "health" event about every 30 seconds.
import { useSyncExternalStore } from "react";
import { api, on, type Cost, type Health, type HealthView, type IdleInfo } from "./api";
import { getState, retryNow, setState, toast } from "./store";

const empty: HealthView = { health: {}, idle: {}, cost: {}, choices: [60, 120, 240, 480], at: "" };
let view: HealthView = empty;
const listeners = new Set<() => void>();

function set(v: HealthView | null | undefined) {
  if (!v) return;
  view = { ...empty, ...v, health: v.health ?? {}, idle: v.idle ?? {}, cost: v.cost ?? {}, choices: v.choices?.length ? v.choices : empty.choices };
  listeners.forEach((f) => f());
  announceStops();
}

// A machine that stopped itself is said once, wherever the user is in the app; the note
// stays on the machine in the Machines view. The last stop announced is remembered across
// launches, so opening the app twice doesn't say it twice.
function announceStops() {
  for (const i of Object.values(view.idle)) {
    if (!i.note || !i.stoppedAt) continue;
    const key = `sky.idleStop.${i.machine}`;
    try {
      if (localStorage.getItem(key) === i.stoppedAt) continue;
      localStorage.setItem(key, i.stoppedAt);
    } catch {
      continue; // without storage it would be said on every check
    }
    toast("info", `${i.machine}: ${i.note.charAt(0).toLowerCase()}${i.note.slice(1)}`, "Opening a session on it starts it again.", {
      label: "Machine",
      run: () => setState({ view: "machines", machineSheet: i.machine }),
    });
  }
}

let wired = false;
function wire() {
  if (wired) return;
  wired = true;
  on<HealthView>("health", set);
  api.machineHealth().then(set).catch(() => {});
  watchPanes();
}

function subscribe(f: () => void) {
  wire();
  listeners.add(f);
  return () => {
    listeners.delete(f);
  };
}

export function useHealthView(): HealthView {
  return useSyncExternalStore(subscribe, () => view);
}
export const useHealth = (machine: string): Health | undefined => useHealthView().health[machine];
export const useIdle = (machine: string): IdleInfo | undefined => useHealthView().idle[machine];
export const useCost = (machine: string): Cost | undefined => useHealthView().cost[machine];

/** Checks the machines now (after something changed that the next tick would only show later). */
export const refreshHealth = () => api.refreshHealth().then(set).catch(() => {});

/** True when opening a session on this stopped machine starts it (stop-when-idle is on). */
export const startsOnDemand = (v: HealthView, machine: string) => (v.idle[machine]?.minutes ?? 0) > 0;

/** The same for one machine, as a hook: it is stopped, and a session opened on it starts it. */
export const useStartsOnDemand = (machine: string, status?: string) => startsOnDemand(useHealthView(), machine) && status === "stopped";

// ---------- formatting ----------

/** A size in bytes for people: "426 MB", "7.7 GB", "48 GB" (binary units, like df -h). */
export function bytes(n: number): string {
  const gb = 1024 ** 3;
  if (n >= 1024 * gb) return `${(n / (1024 * gb)).toFixed(1)} TB`;
  if (n >= 10 * gb) return `${Math.round(n / gb)} GB`;
  if (n >= gb) return `${(n / gb).toFixed(1)} GB`;
  return `${Math.round(n / 1024 ** 2)} MB`;
}

/** An idle limit or a stretch of minutes: "2h", "45m", "1h 30m". */
export function minutesText(min: number): string {
  const h = Math.floor(min / 60);
  const m = Math.round(min % 60);
  if (!h) return `${m}m`;
  return m ? `${h}h ${m}m` : `${h}h`;
}

/** How long a machine has been up: "12m", "6h 15m", "3d 4h". */
export function uptimeText(sec: number): string {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m`;
}

/** A time of day the way the backend writes its notes: "03:10". */
export const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });

// ---------- a touched pane starts its machine ----------
//
// A pane that was open when its machine stopped itself keeps saying "… is stopped": panes
// retry on their own (also when the computer wakes in the night), so a retry is no sign that
// anyone wants the machine. A click or a key press in the pane is. The backend then starts
// the machine, if it is one that starts on demand.

const asked = new Map<string, number>(); // machine → when the backend was last told

/** Did the event land in a terminal pane: its screen, or a notice shown over it? */
function inPane(target: EventTarget | null): boolean {
  for (let el = target instanceof Element ? target : null; el && el !== document.body; el = el.parentElement) {
    if (el.querySelector(":scope > .term-host")) return true;
  }
  return false;
}

function watchPanes() {
  const touched = (e: Event) => {
    if (!inPane(e.target)) return;
    // After the click has done its own work (it may have focused another pane).
    window.setTimeout(() => {
      const s = getState();
      const t = s.tabs.find((x) => x.key === s.activeTab);
      if (!t || t.kind === "local" || !t.machine || t.url) return;
      const machine = t.machine;
      const stopped = s.machines.find((m) => m.machine.name === machine)?.machine.status === "stopped";
      if (!stopped || !startsOnDemand(view, machine)) return;
      if (Date.now() - (asked.get(machine) ?? 0) < 5000) return;
      asked.set(machine, Date.now());
      api
        .paneIntent(machine)
        .then((starting) => {
          // Panes waiting on it ask again now, and show that it is on its way.
          if (starting) for (const x of getState().tabs) if (x.machine === machine && x.retry) retryNow(x.key);
        })
        .catch(() => {});
    }, 0);
  };
  document.addEventListener("click", touched, true);
  document.addEventListener("keydown", touched, true);
}
