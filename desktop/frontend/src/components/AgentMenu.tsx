// The coding agents a session can start with (see the engine's Agents), and the menu rows
// that start one on a machine. Which agents a machine has is asked once and kept, so menus
// can grey out the missing ones without waiting.
import { call } from "../lib/api";
import { LOCAL, openAgentTab } from "../lib/store";
import { AGENT_NAMES } from "../lib/util";
import { AgentIcon } from "./Brand";
import type { MenuRow } from "./ContextMenu";

export const AGENTS = ["claude", "codex", "grok", "mantis"] as const;

const known = new Map<string, string[]>(); // machine → the agents installed on it, as last asked

/** The agents installed on a machine, as last asked; undefined before the first answer. */
export const agentsKnownOn = (machine: string) => known.get(machine);

/** Asks a machine which agents it has (and keeps the answer). */
export async function agentsOn(machine: string): Promise<string[]> {
  const list = await call<string[]>("AgentsOn", machine);
  known.set(machine, list);
  return list;
}

/** A row per agent, each starting it on machine (in dir when given). */
export function agentRows(machine: string, dir?: string): MenuRow[] {
  const have = known.get(machine);
  agentsOn(machine).catch(() => {}); // fresh for the next time
  const place = machine === LOCAL ? "this Mac" : machine;
  return AGENTS.map((a) => {
    const missing = !!have && !have.includes(a);
    return {
      label: AGENT_NAMES[a],
      icon: <AgentIcon agent={a} size={13} />,
      hint: missing ? `not on ${place}` : undefined,
      disabled: missing,
      onClick: () => openAgentTab(machine, a, "group", dir),
    };
  });
}
