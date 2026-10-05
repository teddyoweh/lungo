// The prompt composer (⌘E): a proper place to write to the session in front. Several lines,
// files mentioned with @, saved snippets, what you sent before. ⌘↩ sends it; ⌥↩ only puts it
// in the session's input so you can go on editing there.
import { useEffect, useMemo, useRef, useState } from "react";
import { Bookmark, CornerDownLeft, File, History } from "lucide-react";
import { api } from "../lib/api";
import { LOCAL, machineLabel, paneFolder, paneTitle, toast, useStore, type Tab } from "../lib/store";
import { sendToPane, terminals } from "../lib/terminals";
import { cx, fuzzy, mod } from "../lib/util";
import { askText, showMenu, type MenuRow } from "./ContextMenu";
import { Kbd } from "./ui";

interface Snippet {
  id: string;
  name: string;
  text: string;
}

const load = <T,>(key: string, fallback: T): T => {
  try {
    const v = JSON.parse(localStorage.getItem(key) ?? "null");
    return v && typeof v === "object" ? (v as T) : fallback;
  } catch {
    return fallback;
  }
};
const save = (key: string, v: unknown) => {
  try {
    localStorage.setItem(key, JSON.stringify(v));
  } catch {
    /* storage unavailable */
  }
};

const drafts = new Map<string, string>(); // what was being written for each pane, kept while the app runs
const fileLists = new Map<string, Promise<string[]>>(); // a folder's files, asked for once per folder

function filesOf(tab: Tab): Promise<string[]> {
  const machine = tab.kind === "local" ? LOCAL : (tab.machine ?? "");
  const key = `${machine}\n${tab.cwd ?? ""}`;
  let p = fileLists.get(key);
  if (!p) {
    p = tab.cwd ? api.listFiles(machine, tab.cwd).catch(() => []) : Promise.resolve([]);
    fileLists.set(key, p);
    // Files come and go: ask again after a while.
    window.setTimeout(() => fileLists.delete(key), 60_000);
  }
  return p;
}

export function Composer({ onClose }: { onClose: () => void }) {
  const tab = useStore((s) => s.tabs.find((t) => t.key === s.activeTab));
  const key = tab?.key ?? "";
  const [text, setText] = useState(() => drafts.get(key) ?? "");
  const [files, setFiles] = useState<string[] | null>(null);
  const [pick, setPick] = useState(0);
  const [caret, setCaret] = useState(0);
  const [hidden, setHidden] = useState(false); // the file list was dismissed with Esc; typing brings it back
  const area = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    const el = area.current;
    if (!el) return;
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
  }, []);
  useEffect(() => {
    drafts.set(key, text);
  }, [key, text]);

  // "@par" right before the caret: a file is being mentioned.
  const mention = useMemo(() => /(^|\s)@([^\s@]*)$/.exec(text.slice(0, caret)), [text, caret]);
  const query = mention && !hidden ? mention[2] : null;
  useEffect(() => {
    if (query !== null && files === null && tab) filesOf(tab).then(setFiles);
  }, [query, files, tab]);
  const matches = useMemo(() => {
    if (query === null || !files) return [];
    if (!query) return files.slice(0, 8);
    return files
      .map((f) => ({ f, score: fuzzy(query, f) }))
      .filter((x) => x.score >= 0)
      .sort((a, b) => b.score - a.score || a.f.length - b.f.length)
      .slice(0, 8)
      .map((x) => x.f);
  }, [query, files]);
  useEffect(() => setPick(0), [query]);

  if (!tab) return null;

  const close = () => {
    onClose();
    window.setTimeout(() => terminals.get(key)?.focus(), 0);
  };
  const insert = (s: string, replace = 0) => {
    const el = area.current;
    const at = el ? el.selectionStart : text.length;
    const next = text.slice(0, at - replace) + s + text.slice(el ? el.selectionEnd : at);
    setText(next);
    const pos = at - replace + s.length;
    setCaret(pos);
    window.requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(pos, pos);
    });
  };
  const send = (submit: boolean) => {
    const body = text.trim();
    if (!body) return;
    if (!sendToPane(key, body, submit)) return toast("error", "That pane isn't connected right now");
    const past = load<string[]>("sky.prompts", []).filter((p) => p !== body);
    save("sky.prompts", [body, ...past].slice(0, 60));
    drafts.delete(key);
    setText("");
    close();
  };
  const snippets = (e: React.MouseEvent<HTMLElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const list = load<Snippet[]>("sky.snippets", []);
    const rows: MenuRow[] = [
      ...list.map((sn) => ({
        custom: (
          <div className="group flex h-7 items-center rounded-md pr-1 pl-2 text-[12.5px] text-fg hover:bg-hover">
            <button className="min-w-0 flex-1 truncate text-left" title={sn.text} onClick={() => insert(sn.text)}>
              {sn.name}
            </button>
            <button
              title="Delete this snippet"
              className="px-1 text-[11px] text-subtle opacity-0 group-hover:opacity-100 hover:text-bad"
              onClick={() =>
                save(
                  "sky.snippets",
                  load<Snippet[]>("sky.snippets", []).filter((x) => x.id !== sn.id),
                )
              }
            >
              delete
            </button>
          </div>
        ),
      })),
      ...(list.length ? (["sep"] as const) : []),
      {
        label: "Save this as a snippet…",
        icon: <Bookmark size={13} />,
        disabled: !text.trim(),
        onClick: async () => {
          const name = await askText({ title: "Name this snippet", placeholder: "Review checklist", confirm: "Save" });
          if (name) save("sky.snippets", [...load<Snippet[]>("sky.snippets", []), { id: Date.now().toString(36), name, text }]);
          area.current?.focus();
        },
      },
    ];
    showMenu({ clientX: r.left, clientY: r.top - 6 }, rows);
  };
  const history = (e: React.MouseEvent<HTMLElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const past = load<string[]>("sky.prompts", []);
    showMenu(
      { clientX: r.left, clientY: r.top - 6 },
      past.length
        ? past.slice(0, 14).map((p) => ({
            label: p.replace(/\s+/g, " ").slice(0, 64),
            onClick: () => {
              setText(p);
              window.requestAnimationFrame(() => area.current?.focus());
            },
          }))
        : [{ label: "Nothing sent from here yet", disabled: true, onClick: () => {} }],
    );
  };

  const rows = Math.min(18, Math.max(4, text.split("\n").length + 1));
  return (
    <div className="z anim-fade fixed inset-0 z-50 flex items-end justify-center bg-black/35 pb-[11vh]" onMouseDown={(e) => e.target === e.currentTarget && close()}>
      <div className="anim-in relative w-[min(780px,92vw)] rounded-2xl border border-line-strong bg-raised shadow-pop">
        {query !== null && matches.length > 0 && (
          <div className="absolute inset-x-3 bottom-full mb-2 overflow-hidden rounded-xl border border-line-strong bg-raised p-1 shadow-pop">
            {matches.map((f, i) => (
              <button
                key={f}
                onMouseDown={(e) => {
                  e.preventDefault();
                  insert(`${f} `, query.length);
                }}
                onMouseMove={() => setPick(i)}
                className={cx("flex h-7 w-full items-center gap-2 rounded-md px-2 text-left font-mono text-[12px]", i === pick ? "bg-hover text-fg" : "text-muted")}
              >
                <File size={12} className="shrink-0 text-subtle" />
                <span className="truncate">{f}</span>
              </button>
            ))}
          </div>
        )}
        <div className="flex items-center gap-2 px-4 pt-3 text-[11.5px] text-subtle">
          <span className="truncate">
            To <span className="text-muted">{paneTitle(tab)}</span> · {machineLabel(tab.kind === "local" ? LOCAL : tab.machine)}
            {paneFolder(tab) && ` · ${paneFolder(tab)}`}
          </span>
        </div>
        <textarea
          ref={area}
          value={text}
          rows={rows}
          spellCheck={false}
          placeholder="Write to this session…  @ mentions a file from its folder"
          onChange={(e) => {
            setText(e.target.value);
            setCaret(e.target.selectionStart);
            setHidden(false);
          }}
          onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
          onKeyDown={(e) => {
            e.stopPropagation();
            const picking = query !== null && matches.length > 0;
            if (e.key === "Escape") {
              e.preventDefault();
              if (picking) setHidden(true);
              else close();
            } else if (picking && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
              e.preventDefault();
              setPick((i) => (i + (e.key === "ArrowDown" ? 1 : matches.length - 1)) % matches.length);
            } else if (picking && (e.key === "Enter" || e.key === "Tab") && !e.metaKey && !e.altKey) {
              e.preventDefault();
              insert(`${matches[pick]} `, query.length);
            } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              send(true);
            } else if (e.key === "Enter" && e.altKey) {
              e.preventDefault();
              send(false);
            } else if (e.key.toLowerCase() === "e" && e.metaKey) {
              e.preventDefault();
              close();
            }
          }}
          className="block w-full resize-none bg-transparent px-4 pt-2 pb-3 font-mono text-[13px] leading-[1.5] text-fg outline-none placeholder:text-subtle"
        />
        <div className="flex items-center gap-1 border-t border-line px-3 py-2">
          <button onClick={snippets} className="flex h-7 items-center gap-1.5 rounded-md px-2 text-[12px] text-muted hover:bg-hover hover:text-fg">
            <Bookmark size={12} /> Snippets
          </button>
          <button onClick={history} className="flex h-7 items-center gap-1.5 rounded-md px-2 text-[12px] text-muted hover:bg-hover hover:text-fg">
            <History size={12} /> Sent before
          </button>
          <span className="ml-auto flex items-center gap-1.5 text-[11.5px] text-subtle">
            <Kbd>⌥↩</Kbd> put it in the input
          </span>
          <button
            onClick={() => send(true)}
            disabled={!text.trim()}
            className="ml-2 flex h-7 items-center gap-1.5 rounded-md bg-accent px-2.5 text-[12px] font-medium text-accent-fg hover:brightness-110 disabled:opacity-40"
          >
            Send <span className="opacity-80">{mod}↩</span>
            <CornerDownLeft size={12} />
          </button>
        </div>
      </div>
    </div>
  );
}
