import { useEffect, useRef, useState, useSyncExternalStore, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode } from "react";
import { Check, ChevronDown, Copy, Loader2, X } from "lucide-react";
import { api } from "../lib/api";
import { toast, toggleSidebar, useStore } from "../lib/store";
import { NavIcon } from "./NavIcons";
import { cx, mod } from "../lib/util";

// ---------- buttons ----------

type Variant = "primary" | "secondary" | "ghost" | "danger" | "outline";
type BtnProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant;
  size?: "xs" | "sm" | "md";
  icon?: ReactNode;
  loading?: boolean;
  kbd?: string;
};

const variants: Record<Variant, string> = {
  primary: "bg-accent text-accent-fg hover:bg-accent-strong shadow-[inset_0_1px_0_rgba(255,255,255,0.18)]",
  secondary: "bg-active text-fg hover:bg-[color-mix(in_srgb,var(--fg)_12%,transparent)]",
  outline: "border border-line-strong text-fg hover:bg-hover",
  ghost: "text-muted hover:text-fg hover:bg-hover",
  danger: "bg-bad text-white hover:brightness-110",
};
const sizes = {
  xs: "h-6 px-2 text-[12px] gap-1 rounded-md",
  sm: "h-7 px-2.5 text-[12.5px] gap-1.5 rounded-md",
  md: "h-8 px-3 text-[13px] gap-2 rounded-lg",
};

export function Button({ variant = "secondary", size = "sm", icon, loading, kbd, className, children, disabled, type = "button", ...rest }: BtnProps) {
  return (
    <button
      type={type}
      {...rest}
      disabled={disabled || loading}
      className={cx(
        "no-drag inline-flex shrink-0 select-none items-center justify-center font-medium whitespace-nowrap transition-colors duration-100",
        "disabled:pointer-events-none disabled:opacity-45",
        variants[variant],
        sizes[size],
        className,
      )}
    >
      {loading ? <Loader2 size={13} className="spin" /> : icon}
      {children}
      {kbd && <Kbd className="ml-1 opacity-70">{kbd}</Kbd>}
    </button>
  );
}

export function IconButton({
  label,
  className,
  children,
  active,
  type = "button",
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string; active?: boolean }) {
  return (
    <button
      type={type}
      {...rest}
      title={label}
      aria-label={label}
      className={cx(
        "no-drag inline-flex size-7 shrink-0 items-center justify-center rounded-md text-subtle transition-colors hover:bg-hover hover:text-fg disabled:opacity-40",
        active && "bg-active text-fg",
        className,
      )}
    >
      {children}
    </button>
  );
}

export function Kbd({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <kbd
      className={cx(
        "inline-flex h-[18px] min-w-[18px] items-center justify-center rounded border border-line-strong px-1 font-sans text-[10.5px] font-medium text-subtle",
        className,
      )}
    >
      {children}
    </kbd>
  );
}

// ---------- form controls ----------

export function Input({ className, mono, ...rest }: InputHTMLAttributes<HTMLInputElement> & { mono?: boolean }) {
  return (
    <input
      spellCheck={false}
      autoCorrect="off"
      autoCapitalize="off"
      {...rest}
      className={cx(
        "no-drag h-8 rounded-lg border border-line-strong bg-transparent px-2.5 text-[13px] text-fg outline-none transition-colors",
        !/(^|\s)w-/.test(className ?? "") && "w-full",
        "placeholder:text-subtle focus:border-accent focus:ring-3 focus:ring-accent-soft disabled:opacity-50",
        mono && "font-mono text-[12.5px]",
        className,
      )}
    />
  );
}

export function Textarea({ className, ...rest }: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      spellCheck={false}
      {...rest}
      className={cx(
        "no-drag w-full resize-none rounded-lg border border-line-strong bg-transparent px-2.5 py-2 text-[13px] text-fg outline-none",
        "placeholder:text-subtle focus:border-accent focus:ring-3 focus:ring-accent-soft",
        className,
      )}
    />
  );
}

export function Select({
  value,
  onChange,
  options,
  className,
  placeholder,
  disabled,
}: {
  value: string;
  onChange: (v: string) => void;
  options: { value: string; label: string }[];
  className?: string;
  placeholder?: string;
  disabled?: boolean;
}) {
  return (
    <div className={cx("relative", className)}>
      <select
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        className="no-drag h-8 w-full appearance-none rounded-lg border border-line-strong bg-transparent pr-8 pl-2.5 text-[13px] text-fg outline-none focus:border-accent focus:ring-3 focus:ring-accent-soft disabled:opacity-50"
      >
        {placeholder && (
          <option value="" disabled>
            {placeholder}
          </option>
        )}
        {options.map((o) => (
          <option key={o.value} value={o.value} className="bg-raised text-fg">
            {o.label}
          </option>
        ))}
      </select>
      <ChevronDown size={14} className="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-subtle" />
    </div>
  );
}

export function Toggle({ checked, onChange, disabled, label }: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; label?: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cx(
        "no-drag relative inline-flex h-[18px] w-[30px] shrink-0 items-center rounded-full transition-colors duration-150 disabled:opacity-40",
        checked ? "bg-accent" : "bg-line-strong",
      )}
    >
      <span
        className={cx(
          "inline-block size-[14px] rounded-full bg-white shadow-[0_1px_2px_rgba(0,0,0,0.3)] transition-transform duration-150",
          checked ? "translate-x-[14px]" : "translate-x-[2px]",
        )}
      />
    </button>
  );
}

export function Checkbox({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label?: ReactNode }) {
  return (
    <label className="no-drag inline-flex items-center gap-2 text-[13px] text-fg">
      <button
        type="button"
        role="checkbox"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={cx(
          "inline-flex size-4 items-center justify-center rounded border transition-colors",
          checked ? "border-accent bg-accent text-accent-fg" : "border-line-strong hover:border-subtle",
        )}
      >
        {checked && <Check size={11} strokeWidth={3} />}
      </button>
      {label}
    </label>
  );
}

export function Field({ label, hint, children, className }: { label: ReactNode; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <div className={cx("flex flex-col gap-1.5", className)}>
      <div className="text-[12px] font-medium text-muted">{label}</div>
      {children}
      {hint && <div className="text-[11.5px] leading-snug text-subtle">{hint}</div>}
    </div>
  );
}

export function Segmented<T extends string>({
  value,
  onChange,
  options,
}: {
  value: T;
  onChange: (v: T) => void;
  options: { value: T; label: ReactNode }[];
}) {
  return (
    <div className="no-drag inline-flex rounded-lg bg-active p-0.5">
      {options.map((o) => (
        <button
          type="button"
          key={o.value}
          onClick={() => onChange(o.value)}
          className={cx(
            "h-6 rounded-md px-2.5 text-[12px] font-medium transition-colors",
            value === o.value ? "bg-raised text-fg shadow-[0_1px_2px_rgba(0,0,0,0.2)]" : "text-muted hover:text-fg",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

// ---------- display ----------

/**
 * A grouped list: the rows sit in one outlined, rounded panel, with a faint line between rows
 * that starts after their icon. GROUP is the panel; GROUP_ROW goes on each row (its icon
 * column is 36px wide at 16px from the edge).
 */
export const GROUP = "overflow-hidden rounded-xl border border-line";
export const GROUP_ROW =
  "relative px-4 before:pointer-events-none before:absolute before:top-0 before:right-0 before:left-[66px] before:h-px before:bg-[color-mix(in_srgb,var(--fg)_7%,transparent)] first:before:hidden";

const tones = {
  green: "bg-ok",
  amber: "bg-warn",
  red: "bg-bad",
  grey: "bg-subtle",
  blue: "bg-info",
  violet: "bg-violet",
};

// Claude Code's own working glyphs, out and back. ✳ asks for its text form, or macOS draws an emoji.
const SPARK_FRAMES = ["·", "✢", "✳\uFE0E", "✶", "✻", "✽", "✻", "✶", "✳\uFE0E", "✢"];

// One clock for every mark on screen, so they all turn together; it runs only while one is shown.
// With reduced motion the mark holds still on ✻.
const STILL = matchMedia("(prefers-reduced-motion: reduce)").matches;
let sparkFrame = 0;
const sparkSubs = new Set<() => void>();
let sparkTimer: number | undefined;
function subscribeSpark(fn: () => void) {
  sparkSubs.add(fn);
  if (sparkTimer === undefined && !STILL) {
    sparkTimer = window.setInterval(() => {
      sparkFrame = (sparkFrame + 1) % SPARK_FRAMES.length;
      sparkSubs.forEach((f) => f());
    }, 120);
  }
  return () => {
    sparkSubs.delete(fn);
    if (sparkSubs.size) return;
    window.clearInterval(sparkTimer);
    sparkTimer = undefined;
  };
}

/**
 * The "working" mark: Claude's asterisk, cycling ·✢✳✶✻✽ the way Claude Code does in its own
 * terminal. It takes the text colour (green where a session is working).
 */
export function Sparks({ size = 12, className }: { size?: number; className?: string }) {
  const frame = useSyncExternalStore(subscribeSpark, () => sparkFrame);
  return (
    <span role="img" aria-label="Working" style={{ width: size, height: size, fontSize: size }} className={cx("sparks", className)}>
      {STILL ? "✻" : SPARK_FRAMES[frame]}
    </span>
  );
}

export function Dot({ tone, pulse, hollow, className }: { tone: keyof typeof tones; pulse?: boolean; hollow?: boolean; className?: string }) {
  if (hollow) return <span className={cx("inline-block size-2 shrink-0 rounded-full border border-subtle", className)} />;
  return <span className={cx("inline-block size-2 shrink-0 rounded-full", tones[tone], pulse && "dot-working", className)} />;
}

export function Badge({ children, tone = "grey", className }: { children: ReactNode; tone?: keyof typeof tones | "accent"; className?: string }) {
  const map: Record<string, string> = {
    green: "text-ok bg-[color-mix(in_srgb,var(--green)_13%,transparent)]",
    amber: "text-warn bg-[color-mix(in_srgb,var(--amber)_14%,transparent)]",
    red: "text-bad bg-[color-mix(in_srgb,var(--red)_13%,transparent)]",
    grey: "text-muted bg-active",
    blue: "text-info bg-[color-mix(in_srgb,var(--blue)_13%,transparent)]",
    violet: "text-violet bg-[color-mix(in_srgb,var(--violet)_14%,transparent)]",
    accent: "text-accent bg-accent-soft",
  };
  return (
    <span className={cx("inline-flex h-[18px] items-center gap-1 rounded-[5px] px-1.5 text-[11px] font-medium whitespace-nowrap", map[tone], className)}>
      {children}
    </span>
  );
}

export function Spinner({ size = 14, className }: { size?: number; className?: string }) {
  return <Loader2 size={size} className={cx("spin text-subtle", className)} />;
}

export function Card({ children, className, onClick }: { children: ReactNode; className?: string; onClick?: () => void }) {
  return (
    <div onClick={onClick} className={cx("rounded-xl border border-line bg-panel", className)}>
      {children}
    </div>
  );
}

export function SectionTitle({ children, right, className }: { children: ReactNode; right?: ReactNode; className?: string }) {
  return (
    <div className={cx("mb-2 flex items-center justify-between", className)}>
      <h3 className="text-[11.5px] font-semibold tracking-wide text-subtle uppercase">{children}</h3>
      {right}
    </div>
  );
}

export function Empty({ icon, title, body, action }: { icon?: ReactNode; title: string; body?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center px-6 py-14 text-center">
      {icon && <div className="mb-4 flex size-11 items-center justify-center rounded-xl border border-line bg-panel text-muted">{icon}</div>}
      <div className="text-[14px] font-semibold text-fg">{title}</div>
      {body && <div className="mt-1.5 max-w-sm text-[12.5px] leading-relaxed text-muted">{body}</div>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  );
}

export function CopyField({ value, label }: { value: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <div className="no-drag group flex h-8 items-center gap-2 rounded-lg border border-line bg-[color-mix(in_srgb,var(--fg)_3%,transparent)] pr-1 pl-2.5">
      {label && <span className="text-[11.5px] text-subtle">{label}</span>}
      <code className="selectable min-w-0 flex-1 truncate font-mono text-[12px] text-fg">{value}</code>
      <IconButton
        label="Copy"
        className="size-6"
        onClick={async () => {
          await api.copy(value);
          setDone(true);
          toast("info", "Copied", value);
          setTimeout(() => setDone(false), 1200);
        }}
      >
        {done ? <Check size={13} className="text-ok" /> : <Copy size={13} />}
      </IconButton>
    </div>
  );
}

// ---------- overlays ----------

export function Modal({
  open,
  onClose,
  children,
  width = 520,
  className,
  dismissable = true,
}: {
  open: boolean;
  onClose: () => void;
  children: ReactNode;
  width?: number;
  className?: string;
  dismissable?: boolean;
}) {
  useEffect(() => {
    if (!open || !dismissable) return;
    const h = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    window.addEventListener("keydown", h, true);
    return () => window.removeEventListener("keydown", h, true);
  }, [open, onClose, dismissable]);
  if (!open) return null;
  return (
    <div
      className="z anim-fade fixed inset-0 z-50 flex items-start justify-center bg-[var(--overlay)] px-4 pt-[9vh] backdrop-blur-[2px]"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget && dismissable) onClose();
      }}
    >
      <div
        style={{ width }}
        className={cx("anim-in max-h-[82vh] max-w-full overflow-hidden rounded-2xl border border-line-strong bg-raised shadow-pop", className)}
      >
        {children}
      </div>
    </div>
  );
}

export function ModalHeader({ title, subtitle, onClose, icon }: { title: ReactNode; subtitle?: ReactNode; onClose?: () => void; icon?: ReactNode }) {
  return (
    <div className="flex items-start gap-3 border-b border-line px-5 pt-4 pb-3.5">
      {icon && <div className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent">{icon}</div>}
      <div className="min-w-0 flex-1">
        <div className="text-[14px] font-semibold text-fg">{title}</div>
        {subtitle && <div className="mt-0.5 text-[12.5px] text-muted">{subtitle}</div>}
      </div>
      {onClose && (
        <IconButton label="Close" onClick={onClose} className="-mt-0.5 -mr-1.5">
          <X size={15} />
        </IconButton>
      )}
    </div>
  );
}

export function Sheet({ open, onClose, children, width = 460 }: { open: boolean; onClose: () => void; children: ReactNode; width?: number }) {
  useEffect(() => {
    if (!open) return;
    // Esc closes it and goes no further (in a terminal it would interrupt Claude).
    const h = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      onClose();
    };
    window.addEventListener("keydown", h, true);
    return () => window.removeEventListener("keydown", h, true);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="z anim-fade fixed inset-0 z-40 flex justify-end bg-[var(--overlay)]" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div style={{ width }} className="anim-slide flex h-full max-w-full flex-col border-l border-line-strong bg-raised shadow-pop">
        {children}
      </div>
    </div>
  );
}

/** A small dropdown menu anchored to its trigger. */
export function Menu({ trigger, items, align = "right", side = "bottom" }: { trigger: ReactNode; items: (MenuItem | "sep")[]; align?: "left" | "right"; side?: "bottom" | "top" }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const h = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const k = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      setOpen(false);
    };
    window.addEventListener("mousedown", h);
    window.addEventListener("keydown", k, true);
    return () => {
      window.removeEventListener("mousedown", h);
      window.removeEventListener("keydown", k, true);
    };
  }, [open]);
  return (
    <div className="relative" ref={ref}>
      <div onClick={() => setOpen((o) => !o)}>{trigger}</div>
      {open && (
        <div
          className={cx(
            "anim-in absolute z-30 min-w-[200px] rounded-xl border border-line-strong bg-raised p-1 shadow-pop",
            side === "top" ? "bottom-full mb-1" : "top-full mt-1",
            align === "right" ? "right-0" : "left-0",
          )}
        >
          {items.map((it, i) =>
            it === "sep" ? (
              <div key={i} className="my-1 h-px bg-line" />
            ) : (
              <button
                type="button"
                key={i}
                disabled={it.disabled}
                onClick={() => {
                  setOpen(false);
                  it.onClick();
                }}
                className={cx(
                  "flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-[12.5px] transition-colors disabled:opacity-40",
                  it.danger ? "text-bad hover:bg-[color-mix(in_srgb,var(--red)_12%,transparent)]" : "text-fg hover:bg-hover",
                )}
              >
                <span className="flex w-4 justify-center text-subtle">{it.icon}</span>
                <span className="flex-1">{it.label}</span>
                {it.hint && <span className="text-[11px] text-subtle">{it.hint}</span>}
              </button>
            ),
          )}
        </div>
      )}
    </div>
  );
}

export interface MenuItem {
  label: string;
  icon?: ReactNode;
  onClick: () => void;
  danger?: boolean;
  disabled?: boolean;
  hint?: string;
}

/** Live-updating relative timestamps. */
export function useNow(intervalMs = 15000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(t);
  }, [intervalMs]);
  return now;
}

/** Shows or hides the sidebar. It sits right after the window buttons, wherever they are. */
export function SidebarToggle() {
  const shown = useStore((s) => s.sidebar);
  return (
    <IconButton label={`${shown ? "Hide" : "Show"} sidebar (${mod}B)`} className="size-7" onClick={toggleSidebar}>
      <NavIcon name="sidebar" size={17} />
    </IconButton>
  );
}

export function Page({ title, subtitle, actions, children }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  const sidebar = useStore((s) => s.sidebar);
  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="drag flex h-[44px] shrink-0 items-center gap-3 pr-5" style={{ paddingLeft: sidebar ? 20 : "var(--lights-pad)" }}>
        {!sidebar && <SidebarToggle />}
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-[14px] font-semibold text-fg">{title}</h1>
        </div>
        {subtitle && <div className="hidden text-[12px] text-subtle md:block">{subtitle}</div>}
        <div className="flex items-center gap-1.5">{actions}</div>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
    </div>
  );
}
