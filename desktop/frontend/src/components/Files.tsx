// The Files panel: what is on a session's machine, from here. It opens on what was made or
// changed lately where the session works (and on that machine's Desktop and in its
// Downloads, where Claude leaves what it made for you); "Browse" walks its folders. A click
// shows a file on this computer.
import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowDownUp, Check, ChevronRight, CornerLeftUp, File, FileText, Film, Folder, Image as ImageIcon, Music, Search, X } from "lucide-react";
import { api, errText, type RemoteFile } from "../lib/api";
import { closeFiles, fileKind, fileSize, forgetOpened, openedFiles, peekFile, peeking, useFilesPanel, type FilesPanel } from "../lib/peek";
import { machineLabel } from "../lib/store";
import { agoText, cx, fuzzy, tildePath } from "../lib/util";
import { Menu, Spinner } from "./ui";

function KindIcon({ f }: { f: RemoteFile }) {
  if (f.dir) return <Folder size={13} className="shrink-0 text-accent" />;
  const kind = fileKind(f.name);
  const Icon = kind === "image" ? ImageIcon : kind === "video" ? Film : kind === "audio" ? Music : kind === "text" || kind === "pdf" ? FileText : File;
  return <Icon size={13} className="shrink-0 text-subtle" />;
}

const parentOf = (dir: string) => (dir.lastIndexOf("/") > 0 ? dir.slice(0, dir.lastIndexOf("/")) : "/");

type Mode = "recent" | "opened" | "browse";
const MODES: { id: Mode; label: string }[] = [
  { id: "recent", label: "Recent" },
  { id: "opened", label: "Opened" },
  { id: "browse", label: "Browse" },
];

/** How the list is ordered: newest first, by name, biggest first, or by kind (videos together…). */
type Sort = "newest" | "name" | "size" | "kind";
const SORTS: { id: Sort; label: string }[] = [
  { id: "newest", label: "Newest" },
  { id: "name", label: "Name" },
  { id: "size", label: "Size" },
  { id: "kind", label: "Kind" },
];
const SORT_KEY = "sky.files.sort";
function savedSort(mode: Mode): Sort {
  try {
    const v = JSON.parse(localStorage.getItem(SORT_KEY) ?? "{}")[mode];
    if (SORTS.some((x) => x.id === v)) return v;
  } catch {
    /* nothing saved */
  }
  return mode === "browse" ? "name" : "newest";
}
function saveSort(mode: Mode, sort: Sort) {
  try {
    localStorage.setItem(SORT_KEY, JSON.stringify({ ...JSON.parse(localStorage.getItem(SORT_KEY) ?? "{}"), [mode]: sort }));
  } catch {
    /* storage unavailable */
  }
}
const KIND_ORDER = ["video", "image", "audio", "pdf", "text", "other"];
const ext = (name: string) => (name.includes(".") ? name.slice(name.lastIndexOf(".") + 1).toLowerCase() : "");
/** The order of two files for a sort. Folders come first in every order but newest. */
function compare(sort: Sort, a: RemoteFile & { at?: number }, b: RemoteFile & { at?: number }): number {
  if (sort !== "newest" && !!a.dir !== !!b.dir) return a.dir ? -1 : 1;
  const byName = a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" });
  if (sort === "name") return byName;
  if (sort === "size") return (b.dir ? 0 : b.size) - (a.dir ? 0 : a.size) || byName;
  if (sort === "kind") return KIND_ORDER.indexOf(fileKind(a.name)) - KIND_ORDER.indexOf(fileKind(b.name)) || ext(a.name).localeCompare(ext(b.name)) || byName;
  // newest: when it was opened, for the Opened list; when it changed, otherwise
  return (b.at ?? new Date(b.mod).getTime()) - (a.at ?? new Date(a.mod).getTime()) || byName;
}

export function FilesDialog() {
  const panel = useFilesPanel();
  if (!panel) return null;
  return <Files key={`${panel.machine}/${panel.session}/${panel.dir}`} panel={panel} />;
}

function Files({ panel }: { panel: FilesPanel }) {
  const { machine } = panel;
  const [mode, setModeState] = useState<Mode>(panel.browse ? "browse" : "recent");
  const [sort, setSortState] = useState<Sort>(() => savedSort(panel.browse ? "browse" : "recent"));
  const setMode = (m: Mode) => {
    setModeState(m);
    setSortState(savedSort(m));
  };
  const setSort = (v: Sort) => {
    setSortState(v);
    saveSort(mode, v);
  };
  const [home, setHome] = useState(panel.dir); // the session's folder, as the machine says it
  const [dir, setDir] = useState(panel.dir); // the folder being browsed
  const [list, setList] = useState<RemoteFile[] | null>(null);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState("");
  const [pick, setPick] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const rows = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let off = false;
    setList(null);
    setError("");
    setPick(0);
    const load =
      mode === "opened"
        ? Promise.resolve(setList(openedFiles(machine)))
        : mode === "recent"
          ? api.recentFiles(machine, panel.session, panel.dir).then((v) => {
              if (off) return;
              setHome(v.dir);
              if (!dir) setDir(v.dir);
              setList(v.files);
            })
          : api.listDir(machine, dir || "~").then((files) => !off && setList(files));
    load.catch((e) => {
      if (off) return;
      setError(errText(e));
      setList([]);
    });
    return () => {
      off = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, dir, machine, panel.session]);

  const shown = useMemo(() => {
    if (!list) return [];
    const q = filter.trim();
    // Typing ranks by how well a name matches; otherwise the chosen order.
    if (!q) return [...list].sort((a, b) => compare(sort, a, b));
    return list
      .map((f) => ({ f, score: fuzzy(q, f.name) }))
      .filter((x) => x.score >= 0)
      .sort((a, b) => b.score - a.score)
      .map((x) => x.f);
  }, [list, filter, sort]);
  useEffect(() => setPick(0), [filter]);
  useEffect(() => {
    rows.current?.querySelector<HTMLElement>(`[data-row="${pick}"]`)?.scrollIntoView({ block: "nearest" });
  }, [pick]);

  const open = (f: RemoteFile) => {
    if (f.dir) {
      setMode("browse");
      setFilter("");
      setDir(f.path);
      input.current?.focus();
    } else void peekFile(machine, f, { list: shown });
  };
  /** Where a file is, said short: inside the session's folder, or from the home folder. */
  const where = (f: RemoteFile) => {
    const folder = f.path.slice(0, f.path.lastIndexOf("/"));
    if (mode === "browse") return "";
    if (home && folder === home) return "";
    if (home && folder.startsWith(home + "/")) return folder.slice(home.length + 1);
    return tildePath(folder);
  };

  return (
    <div className="z anim-fade fixed inset-0 z-50 flex items-start justify-center bg-black/45 pt-[12vh]" onMouseDown={(e) => e.target === e.currentTarget && closeFiles()}>
      <div
        className="anim-in flex max-h-[70vh] w-[min(640px,92vw)] flex-col overflow-hidden rounded-2xl bg-raised shadow-pop"
        onKeyDown={(e) => {
          if (peeking()) return; // the preview on top has the keyboard
          e.stopPropagation();
          if (e.key === "Escape") {
            e.preventDefault();
            if (filter) setFilter("");
            else closeFiles();
          } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            e.preventDefault();
            if (shown.length) setPick((i) => (i + (e.key === "ArrowDown" ? 1 : shown.length - 1)) % shown.length);
          } else if (e.key === "Enter" && shown[pick]) {
            e.preventDefault();
            open(shown[pick]);
          } else if (e.key === "Backspace" && !filter && mode === "browse" && dir !== "/") {
            e.preventDefault();
            setDir(parentOf(dir));
          }
        }}
      >
        <div className="flex h-[44px] shrink-0 items-center gap-2 pr-2 pl-4">
          <Search size={13} className="shrink-0 text-subtle" />
          <input
            ref={input}
            autoFocus
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            spellCheck={false}
            placeholder={mode === "recent" ? `Made lately on ${machineLabel(machine)}` : mode === "opened" ? `What you opened on ${machineLabel(machine)}` : `Files in ${tildePath(dir)}`}
            className="min-w-0 flex-1 bg-transparent text-[13px] text-fg outline-none placeholder:text-subtle"
          />
          <Menu
            align="right"
            trigger={
              <button title="Sort" className="flex h-7 shrink-0 items-center gap-1 rounded-md px-2 text-[11.5px] text-subtle hover:bg-hover hover:text-fg">
                <ArrowDownUp size={12} />
                {SORTS.find((x) => x.id === sort)?.label}
              </button>
            }
            items={SORTS.map((x) => ({ label: x.label, icon: x.id === sort ? <Check size={13} /> : <span className="w-[13px]" />, onClick: () => (setSort(x.id), input.current?.focus()) }))}
          />
          <div className="flex shrink-0 items-center rounded-lg bg-[color-mix(in_srgb,var(--fg)_6%,transparent)] p-[2px] text-[11.5px]">
            {MODES.map((m) => (
              <button key={m.id} onClick={() => (setMode(m.id), input.current?.focus())} className={cx("h-6 rounded-md px-2.5", mode === m.id ? "bg-active text-fg" : "text-subtle hover:text-fg")}>
                {m.label}
              </button>
            ))}
          </div>
        </div>
        {mode === "browse" && (
          <div className="flex h-[26px] shrink-0 items-center gap-0.5 overflow-x-auto px-3 text-[11.5px] whitespace-nowrap text-subtle">
            <button title="Up a folder (⌫)" disabled={dir === "/"} onClick={() => setDir(parentOf(dir))} className="mr-1 flex size-5 items-center justify-center rounded hover:bg-hover hover:text-fg disabled:opacity-40">
              <CornerLeftUp size={12} />
            </button>
            {tildePath(dir)
              .split("/")
              .filter(Boolean)
              .map((part, i, all) => (
                <span key={i} className="flex items-center gap-0.5">
                  {i > 0 && <ChevronRight size={10} className="opacity-60" />}
                  <button
                    onClick={() => {
                      // Each crumb is that much of the real path.
                      const real = dir.split("/").filter(Boolean);
                      setDir("/" + real.slice(0, real.length - (all.length - 1 - i)).join("/"));
                    }}
                    className={cx("rounded px-1 py-[1px] hover:bg-hover hover:text-fg", i === all.length - 1 && "text-muted")}
                  >
                    {part}
                  </button>
                </span>
              ))}
          </div>
        )}
        <div ref={rows} className="min-h-[120px] flex-1 overflow-y-auto px-1.5 pb-1.5 [scrollbar-width:thin]">
          {list === null ? (
            <div className="flex h-[120px] items-center justify-center">
              <Spinner size={15} className="text-subtle" />
            </div>
          ) : shown.length === 0 ? (
            <div className="px-4 py-10 text-center text-[12px] text-subtle">
              {error ? error : filter ? "Nothing here by that name." : mode === "recent" ? "Nothing made or changed here in the last week. Browse has everything." : mode === "opened" ? "Files you open from here show up in this list." : "This folder is empty."}
            </div>
          ) : (
            shown.map((f, i) => (
              <button
                key={f.path}
                data-row={i}
                onClick={() => open(f)}
                onMouseMove={() => setPick(i)}
                title={f.path}
                className={cx("group/frow flex h-[30px] w-full items-center gap-2.5 rounded-md px-2.5 text-left text-[12.5px]", i === pick ? "bg-hover text-fg" : "text-muted")}
              >
                <KindIcon f={f} />
                <span className="min-w-0 shrink truncate">{f.name}</span>
                {where(f) && <span className="min-w-0 shrink-[3] truncate text-[11px] text-subtle">{where(f)}</span>}
                <span className="ml-auto shrink-0 text-[11px] text-subtle tabular-nums">
                  {!f.dir && `${fileSize(f.size)} · `}
                  {"at" in f && typeof f.at === "number" ? `opened ${agoText(f.at)}` : agoText(new Date(f.mod).getTime())}
                </span>
                {mode === "opened" && (
                  <span
                    role="button"
                    title="Take it off this list"
                    onClick={(e) => {
                      e.stopPropagation();
                      forgetOpened(machine, f.path);
                      setList(openedFiles(machine));
                    }}
                    className="-mr-1 flex size-5 shrink-0 items-center justify-center rounded text-subtle opacity-0 group-hover/frow:opacity-100 hover:text-fg"
                  >
                    <X size={11} />
                  </span>
                )}
              </button>
            ))
          )}
        </div>
      </div>
    </div>
  );
}
