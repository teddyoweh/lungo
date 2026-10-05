// Every pane's terminal, by pane key, for things that read what a pane shows (the switcher's
// previews). Terminals register themselves when created and leave when disposed.
import type { Terminal } from "@xterm/xterm";

export const terminals = new Map<string, Terminal>();

/** How to type into each pane: its key → a function that sends text to the program in it. */
export const senders = new Map<string, (data: string) => void>();

/**
 * Sends text to a pane as a paste (so a program like Claude Code takes several lines as one
 * message instead of running each), then Enter when submit is set.
 */
export function sendToPane(key: string, text: string, submit: boolean): boolean {
  const send = senders.get(key);
  if (!send) return false;
  send(`\x1b[200~${text.replace(/\r\n?/g, "\n")}\x1b[201~`);
  // Enter a moment later: a program reading the paste would take an immediate one as part of it.
  if (submit) window.setTimeout(() => send("\r"), 120);
  return true;
}
