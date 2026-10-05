// A machine's screen, live in the app (see components/Screen). Which machine is being
// looked at, if any.
import { useSyncExternalStore } from "react";

let machine: string | null = null;
const subs = new Set<() => void>();
const subscribe = (f: () => void) => {
  subs.add(f);
  return () => subs.delete(f);
};

export const useScreen = () => useSyncExternalStore(subscribe, () => machine);
export function openScreen(name: string) {
  machine = name;
  subs.forEach((f) => f());
}
export function closeScreen() {
  machine = null;
  subs.forEach((f) => f());
}
/** Shows a machine's screen, or puts it away when it is the one showing. */
export function toggleScreen(name?: string) {
  if (machine || !name) closeScreen();
  else openScreen(name);
}
