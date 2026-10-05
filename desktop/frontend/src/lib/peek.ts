// Files on machines, shown here. A session's files are where its machine is; "peeking" at
// one brings it over and shows it on this computer: by the name a session printed (⌘-click
// in a terminal), from the list of what was made lately, or because a program on the machine
// asked for it (the `peek` command).
import { useSyncExternalStore } from "react";
import { api, errText, on, type RemoteFile, type PeekView } from "./api";
import { LOCAL, type Tab } from "./store";

export interface Peek {
  machine: string;
  name: string; // what is being looked for, then what is shown
  step: "finding" | "fetching" | "ready" | "missing" | "error";
  file?: RemoteFile;
  others?: RemoteFile[]; // other files of that name, when a bare name matched several
  list?: RemoteFile[]; // the list it was opened from: the arrow keys step through it
  view?: PeekView;
  done?: number; // bytes that have arrived
  error?: string;
}

/** The Files panel: what a session's machine has, lately made or by folder. */
export interface FilesPanel {
  machine: string;
  session: string;
  dir: string;
  browse?: boolean; // open on the folder itself rather than on what was made lately
}

let peek: Peek | null = null;
let panel: FilesPanel | null = null;
let turn = 0; // only the latest request may change what is shown
const subs = new Set<() => void>();
const emit = () => subs.forEach((f) => f());
const subscribe = (f: () => void) => {
  subs.add(f);
  return () => subs.delete(f);
};

export const usePeek = () => useSyncExternalStore(subscribe, () => peek);
export const useFilesPanel = () => useSyncExternalStore(subscribe, () => panel);
export const peeking = () => peek !== null;

function set(next: Peek | null) {
  peek = next;
  emit();
}

export function closePeek() {
  turn++;
  set(null);
}

export function openFiles(p: FilesPanel) {
  panel = p;
  emit();
}
/** Where a pane's files are: its machine (this computer for a local pane), its session, its folder. */
export const filesOf = (tab: Tab): FilesPanel => ({ machine: tab.kind === "local" ? LOCAL : (tab.machine ?? ""), session: tab.session ?? "", dir: tab.cwd ?? "" });
/** Opens the Files panel for a pane, or closes it when it is open. */
export function toggleFiles(tab?: Tab) {
  if (panel) return closeFiles();
  if (tab && (tab.kind === "local" || tab.machine)) openFiles(filesOf(tab));
}
export function closeFiles() {
  panel = null;
  emit();
}

// The files you looked at lately, newest first, kept on this computer: the Files panel's
// "Opened" list.
export interface Opened extends RemoteFile {
  machine: string;
  at: number; // when you opened it
}
const OPENED = "sky.files.opened";
export function openedFiles(machine?: string): Opened[] {
  try {
    const list = JSON.parse(localStorage.getItem(OPENED) ?? "[]") as Opened[];
    return Array.isArray(list) ? list.filter((f) => !machine || f.machine === machine) : [];
  } catch {
    return [];
  }
}
function rememberOpened(machine: string, file: RemoteFile) {
  const rest = openedFiles().filter((f) => !(f.machine === machine && f.path === file.path));
  try {
    localStorage.setItem(OPENED, JSON.stringify([{ ...file, machine, at: Date.now() }, ...rest].slice(0, 80)));
  } catch {
    /* storage unavailable */
  }
}
export function forgetOpened(machine: string, path?: string) {
  try {
    localStorage.setItem(OPENED, JSON.stringify(openedFiles().filter((f) => f.machine !== machine || (path !== undefined && f.path !== path))));
  } catch {
    /* storage unavailable */
  }
}

/** Shows a file of a machine: brings it over (a copy from before is used while it is unchanged). */
export async function peekFile(machine: string, file: RemoteFile, extra: Pick<Peek, "others" | "list"> = {}) {
  const mine = ++turn;
  rememberOpened(machine, file);
  set({ machine, name: file.name, step: "fetching", file, done: 0, ...extra });
  try {
    const view = await api.peekFile(machine, file);
    if (mine === turn) set({ machine, name: file.name, step: "ready", file, view, ...extra });
  } catch (e) {
    if (mine === turn) set({ machine, name: file.name, step: "error", file, error: errText(e), ...extra });
  }
}

/** Shows the file a session means by a name it printed ("Creed-promo.mp4", "~/out/a.png", "src/app.ts:12"). */
export async function peekByName(machine: string, session: string, dir: string, name: string) {
  const mine = ++turn;
  set({ machine, name, step: "finding" });
  try {
    const all = await api.findFile(machine, session, dir, name);
    if (mine !== turn) return;
    const found = all.filter((f) => !f.dir);
    if (found.length === 0 && all.length > 0) {
      // A folder: there is nothing to show, but it can be looked into.
      set(null);
      return openFiles({ machine, session, dir: all[0].path, browse: true });
    }
    if (found.length === 0) return set({ machine, name, step: "missing" });
    await peekFile(machine, found[0], { others: found.length > 1 ? found : undefined });
  } catch (e) {
    if (mine === turn) set({ machine, name, step: "error", error: errText(e) });
  }
}

/** The next or previous file of the list the shown one was opened from. */
export function stepPeek(by: 1 | -1) {
  const list = peek?.list?.filter((f) => !f.dir);
  if (!peek || !list || list.length < 2 || !peek.file) return;
  const i = list.findIndex((f) => f.path === peek!.file!.path);
  const next = list[(i + by + list.length) % list.length];
  void peekFile(peek.machine, next, { list: peek.list });
}

on<{ machine: string; path: string; done: number }>("peek:progress", (p) => {
  if (peek?.step === "fetching" && peek.machine === p.machine && peek.file?.path === p.path) set({ ...peek, done: p.done });
});

export type FileKind = "image" | "video" | "audio" | "pdf" | "text" | "other";
const KINDS: Record<Exclude<FileKind, "other" | "text">, string[]> = {
  image: ["png", "jpg", "jpeg", "gif", "webp", "svg", "avif", "bmp", "ico", "heic", "tiff"],
  video: ["mp4", "mov", "m4v", "webm", "mkv"],
  audio: ["mp3", "wav", "m4a", "aac", "ogg", "flac"],
  pdf: ["pdf"],
};
const TEXT =
  "txt md mdx markdown log json jsonl yaml yml toml ini conf cfg env csv tsv xml html htm css scss less js jsx mjs cjs ts tsx go py rb rs java kt swift c h cc cpp hpp cs php sh zsh bash fish sql graphql proto lua pl r dart vue svelte astro tf lock gradle plist diff patch srt vtt".split(
    " ",
  );
const TEXT_NAMES = ["makefile", "dockerfile", "license", "readme", "procfile", "gemfile", "rakefile", "caddyfile", "justfile"];

export function fileKind(name: string): FileKind {
  const base = name.toLowerCase();
  const ext = base.includes(".") ? base.slice(base.lastIndexOf(".") + 1) : "";
  for (const [kind, exts] of Object.entries(KINDS)) if (exts.includes(ext)) return kind as FileKind;
  if (TEXT.includes(ext) || TEXT_NAMES.includes(base) || base.startsWith(".")) return "text";
  return "other";
}

/** Extensions a file name in terminal output is recognised by when it stands alone. */
export const KNOWN_EXTENSIONS = new Set([...Object.values(KINDS).flat(), ...TEXT, "zip", "tar", "gz", "tgz", "dmg", "pkg", "app", "ipa", "apk", "docx", "xlsx", "pptx", "key", "numbers", "pages", "psd", "fig", "sketch", "ttf", "otf", "woff", "woff2", "wasm", "db", "sqlite"]);

export function fileSize(n: number): string {
  if (n < 1000) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1000;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}
