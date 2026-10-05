// A file from a machine, shown on this computer: a video plays, an image and a PDF show, text
// reads. It was brought over when it was asked for (see lib/peek); from here it can be opened
// in its own app, shown in Finder or kept in Downloads.
import { useEffect, useState } from "react";
import { ChevronLeft, ChevronRight, Copy, Download, ExternalLink, FileQuestion, FolderOpen, X } from "lucide-react";
import { api, errText } from "../lib/api";
import { closePeek, fileKind, fileSize, peekFile, stepPeek, usePeek, type Peek } from "../lib/peek";
import { LOCAL, machineLabel, toast } from "../lib/store";
import { agoText, cx, tildePath } from "../lib/util";
import { IconButton, Spinner } from "./ui";

const TEXT_LIMIT = 1_500_000; // bytes of a text file that are read to show it

const folderOf = (path: string) => tildePath(path.slice(0, Math.max(1, path.lastIndexOf("/"))));

export function PeekOverlay() {
  const peek = usePeek();
  const open = peek !== null;
  // The keyboard is the preview's while it is up. The terminal behind lets go of it (a key
  // typed into it would go to Claude) and gets it back when the preview closes.
  useEffect(() => {
    if (!open) return;
    const had = document.activeElement as HTMLElement | null;
    had?.blur();
    return () => had?.focus?.();
  }, [open]);
  useEffect(() => {
    if (!peek) return;
    const key = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey) return; // the app's shortcuts go on working
      e.stopPropagation();
      if (e.key === "Escape") closePeek();
      else if (e.key === "ArrowRight") stepPeek(1);
      else if (e.key === "ArrowLeft") stepPeek(-1);
      else if (e.key === "Enter" && peek.view) void api.openPeeked(peek.view.local);
      else return; // anything else is the preview's own (space plays and pauses a video)
      e.preventDefault();
    };
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, [peek]);
  if (!peek) return null;
  const many = (peek.list?.filter((f) => !f.dir).length ?? 0) > 1;
  return (
    <div className="z anim-fade fixed inset-0 z-[70] flex flex-col items-center justify-center bg-black/70 p-[4vh] backdrop-blur-sm" onMouseDown={(e) => e.target === e.currentTarget && closePeek()}>
      <div className="anim-in flex max-h-full w-fit max-w-full min-w-[min(520px,92vw)] flex-col overflow-hidden rounded-2xl bg-raised shadow-pop">
        <Header peek={peek} />
        <div className="relative flex min-h-[180px] flex-1 items-center justify-center overflow-hidden bg-black/30">
          <Body peek={peek} />
          {many && (
            <>
              <button title="Previous (←)" onClick={() => stepPeek(-1)} className="absolute top-1/2 left-2 flex size-8 -translate-y-1/2 items-center justify-center rounded-full bg-black/45 text-white/80 opacity-0 transition hover:bg-black/70 hover:text-white [div:hover>&]:opacity-100">
                <ChevronLeft size={16} />
              </button>
              <button title="Next (→)" onClick={() => stepPeek(1)} className="absolute top-1/2 right-2 flex size-8 -translate-y-1/2 items-center justify-center rounded-full bg-black/45 text-white/80 opacity-0 transition hover:bg-black/70 hover:text-white [div:hover>&]:opacity-100">
                <ChevronRight size={16} />
              </button>
            </>
          )}
        </div>
        {peek.others && peek.others.length > 1 && (
          <div className="flex w-0 min-w-full shrink-0 items-center gap-1 overflow-x-auto px-3 py-2 text-[11.5px] text-subtle">
            <span className="shrink-0 pr-1">{peek.others.length} files have this name:</span>
            {peek.others.map((f) => (
              <button
                key={f.path}
                title={f.path}
                onClick={() => void peekFile(peek.machine, f, { others: peek.others })}
                className={cx("shrink-0 rounded-md px-2 py-1 hover:bg-hover hover:text-fg", f.path === peek.file?.path && "bg-active text-fg")}
              >
                {folderOf(f.path)}
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function Header({ peek }: { peek: Peek }) {
  const f = peek.file;
  const local = peek.view?.local;
  const here = peek.machine === LOCAL;
  const act = (what: Promise<unknown>, done?: string) => what.then(() => done && toast("info", done), (e) => toast("error", "That didn't work", errText(e)));
  return (
    // w-0 min-w-full: as wide as what is shown, never widening it (a long path is cut instead)
    <div className="flex h-[46px] w-0 min-w-full shrink-0 items-center gap-3 pr-2 pl-4">
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] font-medium text-fg">{peek.name}</div>
        <div className="truncate text-[11px] text-subtle">
          {machineLabel(peek.machine)}
          {f && ` · ${folderOf(f.path)} · ${fileSize(f.size)} · ${agoText(new Date(f.mod).getTime())}`}
        </div>
      </div>
      {local && (
        <div className="flex shrink-0 items-center gap-0.5">
          <IconButton label="Open in its app (↩)" className="size-7" onClick={() => act(api.openPeeked(local))}>
            <ExternalLink size={14} />
          </IconButton>
          {!here && (
            <IconButton label="Keep it: save to Downloads" className="size-7" onClick={() => act(api.savePeeked(local), "Saved to Downloads")}>
              <Download size={14} />
            </IconButton>
          )}
          <IconButton label={here ? "Show in Finder" : "Show the copy in Finder"} className="size-7" onClick={() => act(api.revealPeeked(local))}>
            <FolderOpen size={14} />
          </IconButton>
          {f && (
            <IconButton label={`Copy its path on ${machineLabel(peek.machine)}`} className="size-7" onClick={() => act(api.copy(f.path), "Path copied")}>
              <Copy size={14} />
            </IconButton>
          )}
        </div>
      )}
      <IconButton label="Close (Esc)" className="size-7" onClick={closePeek}>
        <X size={15} />
      </IconButton>
    </div>
  );
}

function Body({ peek }: { peek: Peek }) {
  const [broken, setBroken] = useState(false);
  useEffect(() => setBroken(false), [peek.view?.url]);
  const note = "flex w-[min(520px,88vw)] flex-col items-center gap-3 px-8 py-14 text-center text-[12.5px] text-muted";
  if (peek.step === "finding")
    return (
      <div className={note}>
        <Spinner size={16} className="text-subtle" />
        Looking for {peek.name} on {machineLabel(peek.machine)}…
      </div>
    );
  if (peek.step === "missing")
    return (
      <div className={note}>
        <FileQuestion size={22} className="text-subtle" />
        <span>
          No file named <span className="text-fg">{peek.name}</span> on {machineLabel(peek.machine)}.
        </span>
        <span className="text-subtle">Looked in the session's folder, the Desktop, Downloads, Documents and the home folder.</span>
      </div>
    );
  if (peek.step === "error")
    return (
      <div className={note}>
        <span className="text-bad">Couldn't get it</span>
        <span className="text-subtle">{peek.error}</span>
      </div>
    );
  if (peek.step === "fetching" || !peek.view || !peek.file) {
    const size = peek.file?.size ?? 0;
    const pct = size ? Math.min(100, Math.round(((peek.done ?? 0) / size) * 100)) : 0;
    return (
      <div className={note}>
        <span>
          Bringing it over from {machineLabel(peek.machine)}
          {size > 300_000 ? ` · ${fileSize(peek.done ?? 0)} of ${fileSize(size)}` : "…"}
        </span>
        <div className="h-[3px] w-56 overflow-hidden rounded-full bg-active">
          <div className="h-full rounded-full bg-accent transition-[width] duration-200" style={{ width: `${Math.max(4, pct)}%` }} />
        </div>
      </div>
    );
  }
  const { url, local } = peek.view;
  const kind = fileKind(peek.file.name);
  const fallback = (why: string) => (
    <div className={note}>
      <FileQuestion size={22} className="text-subtle" />
      <span>{why}</span>
      <button onClick={() => void api.openPeeked(local)} className="mt-1 flex h-8 items-center gap-2 rounded-lg bg-accent px-3 text-[12.5px] font-medium text-accent-fg hover:brightness-110">
        <ExternalLink size={13} /> Open in its app
      </button>
    </div>
  );
  if (broken) return fallback("This one can't be shown here.");
  const media = "max-h-[calc(92vh-46px-8vh)] max-w-[92vw]";
  if (kind === "image") return <img src={url} alt={peek.name} onError={() => setBroken(true)} className={cx(media, "object-contain")} />;
  if (kind === "video") return <video key={url} src={url} controls autoPlay onError={() => setBroken(true)} className={cx(media, "bg-black outline-none")} />;
  if (kind === "audio")
    return (
      <div className="px-10 py-12">
        <audio key={url} src={url} controls autoPlay onError={() => setBroken(true)} className="w-[min(460px,80vw)]" />
      </div>
    );
  if (kind === "pdf") return <iframe key={url} src={url} title={peek.name} className="h-[calc(92vh-46px-8vh)] w-[min(980px,92vw)] bg-white" />;
  if (kind === "text") return <TextBody url={url} size={peek.file.size} onBroken={() => setBroken(true)} />;
  return fallback("No preview for this kind of file.");
}

function TextBody({ url, size, onBroken }: { url: string; size: number; onBroken: () => void }) {
  const [text, setText] = useState<string | null>(null);
  useEffect(() => {
    let off = false;
    setText(null);
    fetch(url, size > TEXT_LIMIT ? { headers: { Range: `bytes=0-${TEXT_LIMIT - 1}` } } : undefined)
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(String(r.status)))))
      .then((t) => !off && setText(t))
      .catch(() => !off && onBroken());
    return () => {
      off = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [url]);
  if (text === null)
    return (
      <div className="px-8 py-14">
        <Spinner size={16} className="text-subtle" />
      </div>
    );
  return (
    <div className="h-full max-h-[calc(92vh-46px-8vh)] w-[min(980px,92vw)] overflow-auto">
      <pre className="min-w-full px-5 py-4 font-mono text-[12px] leading-[1.55] whitespace-pre text-fg select-text">{text}</pre>
      {size > TEXT_LIMIT && <div className="px-5 pb-4 text-[11.5px] text-subtle">The first {fileSize(TEXT_LIMIT)} of {fileSize(size)}. Open it in its app for the rest.</div>}
    </div>
  );
}
