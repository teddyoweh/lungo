// Projects: a repo on this computer and the same repo on a machine, and moving work between
// them. The Projects view and the pane/palette actions both go through this module.
//
// A handoff is exact: branch, commits (pushed or not) and uncommitted work, plus the .env
// files a repo needs. Nothing changes on the side the work comes from.
import { api, call, errText, type OpEnd } from "./api";
import { openLocalTab, openSessionTab, runOp, toast, waitOp } from "./store";

/** One copy of a project: here, or on a machine. */
export interface ProjectSide {
  dir: string; // ~/code/api
  path: string; // absolute on that side
  branch: string; // "" when detached
  head: string; // short hash
  subject: string;
  commitAt: string;
  hasUpstream: boolean;
  ahead: number; // commits not on its upstream ("unpushed")
  behind: number;
  changed: number;
  untracked: number;
}

export type ProjectState = "synced" | "local-ahead" | "remote-ahead" | "diverged" | "only-remote" | "missing" | "unreachable";

/** A project's copy on one machine and how it relates to the copy here. */
export interface ProjectCopy {
  machine: string;
  side: ProjectSide | null;
  state: ProjectState;
  text: string; // the state in words
  action: "send" | "bring" | "open" | "";
  localCommits: number; // -1 = unknown
  remoteCommits: number;
}

export interface Project {
  id: string;
  name: string;
  folder: string; // folder name when it differs from the repo's name
  origin: string; // github.com/owner/repo
  host: "github" | "gitlab" | "bitbucket" | "git" | "";
  local: ProjectSide | null;
  copies: ProjectCopy[];
  saved: boolean;
}

export interface ProjectList {
  projects: Project[];
  machines: string[]; // machines that could be read
  errors: Record<string, string>;
  roots: string[];
  at: string;
  stale: boolean;
}

/** What a send or bring did. */
export interface HandoffResult {
  name: string;
  machine: string;
  localDir: string;
  remoteDir: string;
  branch: string;
  created: "" | "cloned" | "init";
  stashed: boolean; // the destination had uncommitted work; it is in its stash
  backup: string; // branch holding destination commits the incoming branch lacked
  files: string[] | null; // ignored files copied (.env…)
}

export interface HandoffOptions {
  claude?: boolean; // open a Claude session in the folder afterwards (default true)
  open?: boolean; // open a pane on the other side afterwards (default true)
  to?: string; // destination folder (default: remembered, found by origin, or ~/code/<name>)
}

export const projectsApi = {
  /** fresh=false answers at once from the last read (check .stale and ask again). */
  list: (fresh = false) => call<ProjectList>("Projects", fresh),
  add: (dir: string) => call<string>("ProjectAdd", dir),
  forget: (key: string, machine = "") => call<void>("ProjectForget", key, machine),
  setRemote: (localDir: string, machine: string, remoteDir: string) => call<void>("ProjectSetRemote", localDir, machine, remoteDir),
};

/**
 * Opens a pane in a folder: on a machine (a Claude session, or a plain tmux session) or on
 * this computer. This is the one place that knows how; swap its body for the folder-aware
 * pane functions when they exist.
 */
export async function openPaneIn(machine: string | null, dir: string, claude = true): Promise<void> {
  if (!machine) {
    openLocalTab("group", dir, claude);
    return;
  }
  const s = await api.newSession(machine, { name: "", dir, claude, args: "", prompt: "" });
  openSessionTab(machine, s.name);
}

function kept(r: HandoffResult, where: string): string | undefined {
  const notes: string[] = [];
  if (r.stashed) notes.push(`Uncommitted work that was on ${where} is in its git stash.`);
  if (r.backup) notes.push(`Commits only ${where} had are on the branch ${r.backup}.`);
  if (r.files?.length) notes.push(`Copied ${r.files.join(", ")}.`);
  return notes.length ? notes.join(" ") : undefined;
}

async function finish(id: string): Promise<HandoffResult | null> {
  const end: OpEnd = await waitOp(id);
  if (end.error) return null; // the op's failure toast (with details) is already showing
  return end.result as HandoffResult;
}

/** Send the repo at localDir (uncommitted work included) to a machine and open it there. */
export async function continueOn(machine: string, localDir: string, opts: HandoffOptions = {}): Promise<void> {
  const id = await runOp(call<string>("ProjectSend", machine, localDir, { to: opts.to ?? "" }), true);
  const r = await finish(id);
  if (!r) return;
  toast("success", `${r.name} is on ${machine}`, kept(r, machine) ?? `${r.remoteDir}${r.branch ? ` · ${r.branch}` : ""}, exactly as on this Mac`);
  if (opts.open === false) return;
  try {
    await openPaneIn(machine, r.remoteDir, opts.claude !== false);
  } catch (e) {
    toast("error", `Couldn't open ${r.name} on ${machine}`, errText(e));
  }
}

/** Bring the repo at remoteDir on a machine (uncommitted work included) to this computer. */
export async function bringHere(machine: string, remoteDir: string, opts: HandoffOptions = {}): Promise<void> {
  const id = await runOp(call<string>("ProjectBring", machine, remoteDir, { to: opts.to ?? "" }), true);
  const r = await finish(id);
  if (!r) return;
  toast("success", `${r.name} is on this Mac`, kept(r, "this Mac") ?? `${r.localDir.replace(/^\/Users\/[^/]+/, "~")}${r.branch ? ` · ${r.branch}` : ""}, exactly as on ${machine}`);
  if (opts.open === false) return;
  try {
    await openPaneIn(null, r.localDir, opts.claude !== false);
  } catch (e) {
    toast("error", `Couldn't open ${r.name} here`, errText(e));
  }
}
