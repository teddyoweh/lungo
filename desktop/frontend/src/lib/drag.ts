// A session being dragged from the sidebar onto the panes. The panes show where it would go
// while it is over them; this is the little state both sides share.
import { useSyncExternalStore } from "react";

/** A session from the sidebar, or a pane being moved by its title bar. */
export interface Dragged {
  machine?: string;
  session?: string;
  pane?: string; // a pane's key: it moves (out of its split, or next to another pane)
}

let dragged: Dragged | null = null;
const listeners = new Set<() => void>();

export function setDragged(d: Dragged | null) {
  dragged = d;
  listeners.forEach((l) => l());
}

export const getDragged = () => dragged;

export function useDragged(): Dragged | null {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => dragged,
  );
}
