// A menu at the pointer (right-click on a tab, a session…) and a one-line question
// ("Rename…"). Both are opened from anywhere with a function call and drawn once, here.
import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { cx } from "../lib/util";

export interface MenuEntry {
  label: string;
  icon?: ReactNode;
  hint?: string;
  danger?: boolean;
  disabled?: boolean;
  checked?: boolean;
  onClick: () => void;
}
/** An entry, a separator, or anything drawn as a row of its own (colour swatches). */
export type MenuRow = MenuEntry | "sep" | { custom: ReactNode };

interface Ask {
  title: string;
  body?: string; // a yes/no question: no text field, just the two buttons
  danger?: boolean;
  value: string;
  placeholder?: string;
  confirm: string;
  done: (v: string | null) => void;
}

let menu: { x: number; y: number; rows: MenuRow[] } | null = null;
let ask: Ask | null = null;
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

/** Opens a menu where the pointer is. */
export function showMenu(e: { clientX: number; clientY: number; preventDefault?: () => void }, rows: MenuRow[]) {
  e.preventDefault?.();
  menu = { x: e.clientX, y: e.clientY, rows: rows.filter((r, i, all) => r !== "sep" || (i > 0 && i < all.length - 1 && all[i - 1] !== "sep")) };
  emit();
}
export function closeMenu() {
  if (!menu) return;
  menu = null;
  emit();
}

/** Asks for one line of text. Resolves with it (trimmed; may be empty), or null when cancelled. */
export function askText(o: { title: string; value?: string; placeholder?: string; confirm?: string }): Promise<string | null> {
  return new Promise((resolve) => {
    ask?.done(null);
    ask = { title: o.title, value: o.value ?? "", placeholder: o.placeholder, confirm: o.confirm ?? "Save", done: resolve };
    emit();
  });
}
/** Asks a yes/no question. */
export function askConfirm(o: { title: string; body: string; confirm: string; danger?: boolean }): Promise<boolean> {
  return new Promise((resolve) => {
    ask?.done(null);
    ask = { title: o.title, body: o.body, danger: o.danger, value: "", confirm: o.confirm, done: (v) => resolve(v !== null) };
    emit();
  });
}
function answer(v: string | null) {
  const a = ask;
  ask = null;
  emit();
  a?.done(v);
}

export function ContextMenuHost() {
  const m = useSyncExternalStore(subscribe, () => menu);
  const a = useSyncExternalStore(subscribe, () => ask);
  return (
    <>
      {m && <MenuAt key={`${m.x}:${m.y}`} x={m.x} y={m.y} rows={m.rows} />}
      {a && <AskBox key={a.title} a={a} />}
    </>
  );
}

function MenuAt({ x, y, rows }: { x: number; y: number; rows: MenuRow[] }) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: x, top: y, ready: false });
  // Keep it on screen: flip to the other side of the pointer near an edge. The page is zoomed
  // (.z), and engines differ on whether that scales left/top: measure where two positions
  // land instead of assuming (as Menu does).
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.left = el.style.top = "0px";
    const a = el.getBoundingClientRect();
    el.style.left = el.style.top = "100px";
    const b = el.getBoundingClientRect();
    const kx = (b.left - a.left) / 100 || 1;
    const ky = (b.top - a.top) / 100 || 1;
    const left = x + a.width > window.innerWidth - 8 ? Math.max(8, x - a.width) : x;
    const top = y + a.height > window.innerHeight - 8 ? Math.max(8, y - a.height) : y;
    setPos({ left: (left - a.left) / kx, top: (top - a.top) / ky, ready: true });
  }, [x, y]);
  useEffect(() => {
    const down = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) closeMenu();
    };
    // Esc closes the menu and goes no further: in a terminal it would interrupt Claude.
    const key = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      e.stopPropagation();
      closeMenu();
    };
    window.addEventListener("mousedown", down, true);
    window.addEventListener("keydown", key, true);
    window.addEventListener("blur", closeMenu);
    return () => {
      window.removeEventListener("mousedown", down, true);
      window.removeEventListener("keydown", key, true);
      window.removeEventListener("blur", closeMenu);
    };
  }, []);
  return (
    <div
      ref={ref}
      className={cx("z fixed z-[80] min-w-[190px] rounded-xl border border-line-strong bg-raised p-1 shadow-pop", pos.ready ? "anim-in" : "invisible")}
      style={{ left: pos.left, top: pos.top }}
      onContextMenu={(e) => e.preventDefault()}
    >
      {rows.map((it, i) =>
        it === "sep" ? (
          <div key={i} className="my-1 h-px bg-line" />
        ) : "custom" in it ? (
          <div key={i} onClick={closeMenu}>
            {it.custom}
          </div>
        ) : (
          <button
            type="button"
            key={i}
            disabled={it.disabled}
            onClick={() => {
              closeMenu();
              it.onClick();
            }}
            className={cx(
              "flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-[12.5px] transition-colors disabled:opacity-40",
              it.danger ? "text-bad hover:bg-[color-mix(in_srgb,var(--red)_12%,transparent)]" : "text-fg hover:bg-hover",
            )}
          >
            <span className="flex w-4 justify-center text-subtle">{it.icon}</span>
            <span className="min-w-0 flex-1 truncate">{it.label}</span>
            {it.checked && <span className="text-[11px] text-accent">✓</span>}
            {it.hint && <span className="text-[11px] text-subtle">{it.hint}</span>}
          </button>
        ),
      )}
    </div>
  );
}

function AskBox({ a }: { a: Ask }) {
  const [v, setV] = useState(a.value);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, []);
  return (
    <div
      className="z anim-fade fixed inset-0 z-[90] flex items-start justify-center bg-black/45 pt-[22vh]"
      onMouseDown={() => answer(null)}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.key === "Escape") answer(null);
      }}
    >
      <form
        onMouseDown={(e) => e.stopPropagation()}
        onSubmit={(e) => {
          e.preventDefault();
          answer(v.trim());
        }}
        className="anim-in w-[360px] rounded-xl border border-line-strong bg-raised p-3 shadow-pop"
      >
        <div className="mb-2 text-[12.5px] font-medium text-fg">{a.title}</div>
        {a.body && <div className="text-[12px] leading-snug text-muted">{a.body}</div>}
        <input
          ref={input}
          hidden={!!a.body}
          value={v}
          placeholder={a.placeholder}
          onChange={(e) => setV(e.target.value)}
          onKeyDown={(e) => {
            e.stopPropagation();
            if (e.key === "Escape") answer(null);
            else if (e.key === "Enter") {
              e.preventDefault();
              answer(v.trim());
            }
          }}
          className="h-8 w-full rounded-md border border-line-strong bg-[var(--bg)] px-2.5 text-[13px] text-fg outline-none placeholder:text-subtle focus:border-accent"
        />
        <div className="mt-3 flex justify-end gap-2">
          <button type="button" onClick={() => answer(null)} className="h-7 rounded-md px-2.5 text-[12px] text-muted hover:bg-hover hover:text-fg">
            Cancel
          </button>
          <button type="submit" autoFocus={!!a.body} className={cx("h-7 rounded-md px-3 text-[12px] font-medium hover:brightness-110", a.danger ? "bg-bad text-white" : "bg-accent text-accent-fg")}>
            {a.confirm}
          </button>
        </div>
      </form>
    </div>
  );
}
