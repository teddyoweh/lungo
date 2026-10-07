// Brand logos: clouds, Claude, GitHub and the CLI tools sync copies logins for.
import { useId, useMemo, type ReactNode } from "react";
import { Folder, KeyRound, Laptop, Server, TerminalSquare } from "lucide-react";
import { BRANDS, type BrandName } from "../lib/brands";
import type { Credential, Machine, Session } from "../lib/api";
import { useStore } from "../lib/store";
import { AGENT_NAMES, agentOf, cx, sessionTone, statusTone } from "../lib/util";
import { Dot, WorkingMark } from "./ui";

export function isBrand(name?: string): name is BrandName {
  return !!name && name in BRANDS;
}

/** A brand logo as inline SVG. Black parts follow the text colour, so they read in dark mode. */
export function BrandIcon({ name, size = 16, className, title }: { name: BrandName; size?: number; className?: string; title?: string }) {
  const b = BRANDS[name];
  // A logo's gradients and masks are found by ID, and the first element with an ID wins: two
  // copies of a logo on the page (one in a hidden view) would both point at the first, and
  // show nothing while it is hidden. So each copy gets IDs of its own.
  const uid = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const body = useMemo(() => (b.b.includes('id="') ? b.b.replace(/(id="|url\(#|href="#)([^")]+)/g, `$1${uid}-$2`) : b.b), [b.b, uid]);
  return (
    <svg
      viewBox={b.v}
      width={size}
      height={size}
      role="img"
      aria-label={title ?? name}
      className={cx("shrink-0", className)}
      dangerouslySetInnerHTML={{ __html: (title ? `<title>${escapeXML(title)}</title>` : "") + body }}
    />
  );
}

function escapeXML(s: string) {
  return s.replace(/[<>&"]/g, (c) => ({ "<": "&lt;", ">": "&gt;", "&": "&amp;", '"': "&quot;" })[c]!);
}

export const PROVIDER_NAMES: Record<string, string> = { gcp: "Google Cloud", aws: "Amazon Web Services", azure: "Microsoft Azure", ssh: "Your machine" };

/** The cloud a machine runs on; machines added by SSH show their OS. */
export function ProviderIcon({ provider, os, size = 14, className }: { provider?: string; os?: string; size?: number; className?: string }) {
  if (provider === "gcp" || provider === "aws" || provider === "azure") return <BrandIcon name={provider} size={size} className={className} title={PROVIDER_NAMES[provider]} />;
  if (os === "darwin") return <BrandIcon name="apple" size={size} className={className} title="macOS" />;
  if (os === "linux") return <BrandIcon name="linux" size={size} className={className} title="Linux" />;
  return <Server size={size} className={cx("text-subtle", className)} />;
}

/** Provider logo with the machine's status as a small badge in the corner. */
export function MachineIcon({ m, size = 14, className }: { m: Machine; size?: number; className?: string }) {
  return (
    <span className={cx("relative inline-flex shrink-0", className)}>
      <ProviderIcon provider={m.provider} os={m.os} size={size} />
      <Badge>
        <Dot tone={statusTone(m.status)} className="size-[6px]" />
      </Badge>
    </span>
  );
}

/** This computer: Apple logo on a Mac, Tux on Linux, a laptop otherwise. */
export function LocalIcon({ size = 14, className }: { size?: number; className?: string }) {
  const platform = useStore((s) => s.info?.platform);
  if (platform === "darwin") return <BrandIcon name="apple" size={size} className={className} title="This Mac" />;
  if (platform === "linux") return <BrandIcon name="linux" size={size} className={className} title="This computer" />;
  return <Laptop size={size} className={cx("text-subtle", className)} />;
}

/** Each agent's mark: Codex has OpenAI's. */
const AGENT_BRAND: Record<string, BrandName> = { claude: "claude", codex: "openai", grok: "grok", mantis: "mantis" };

/** A coding agent's logo; a terminal for a shell. */
export function AgentIcon({ agent, size = 14, className }: { agent?: string; size?: number; className?: string }) {
  const brand = agent ? AGENT_BRAND[agent] : undefined;
  if (!brand) return <TerminalSquare size={size} className={cx("text-subtle", className)} />;
  return <BrandIcon name={brand} size={size} title={AGENT_NAMES[agent!] ?? agent} className={cx(agent === "codex" || agent === "grok" ? "text-fg" : undefined, className)} />;
}

/**
 * A session: the Claude logo for Claude sessions, a terminal otherwise, with its state as a
 * badge. While Claude works the icon is the loader itself: green segments.
 */
export function SessionIcon({ s, size = 13, className }: { s?: Session; size?: number; className?: string }) {
  const tone = sessionTone(s);
  if (tone === "working") return <WorkingMark size={size} fit className={cx("text-ok", className)} />;
  return (
    <span className={cx("relative inline-flex shrink-0", className)}>
      <AgentIcon agent={agentOf(s)} size={size} />
      {tone !== "plain" && <Badge>{tone === "waiting" ? <Dot tone="amber" className="size-[6px]" /> : <Dot tone="grey" className="size-[6px]" />}</Badge>}
    </span>
  );
}

function Badge({ children }: { children: ReactNode }) {
  return <span className="absolute -right-[3px] -bottom-[3px] flex rounded-full p-[1.5px]" style={{ background: "var(--bg)" }}>{children}</span>;
}

/** A rounded tile that frames a logo, for list rows and cards. */
export function IconTile({ children, size = 30, className }: { children: ReactNode; size?: number; className?: string }) {
  return (
    <span
      className={cx("inline-flex shrink-0 items-center justify-center rounded-lg border border-line bg-panel", className)}
      style={{ width: size, height: size }}
    >
      {children}
    </span>
  );
}

/** The icon for a sync item. */
export function SyncItemIcon({ id, size = 16 }: { id: string; size?: number }) {
  switch (id) {
    case "claude":
      return <BrandIcon name="claude" size={size} title="Claude Code" />;
    case "claude-login":
      return (
        <span className="relative inline-flex">
          <BrandIcon name="claude" size={size} title="Claude login" />
          <span className="absolute -right-1 -bottom-1 flex rounded-full p-[1.5px]" style={{ background: "var(--panel)" }}>
            <KeyRound size={Math.round(size * 0.55)} className="text-warn" />
          </span>
        </span>
      );
    case "github":
      return <BrandIcon name="github" size={size} title="GitHub" className="text-fg" />;
    case "git":
      return <BrandIcon name="git" size={size} title="Git" />;
    case "env":
      return <KeyRound size={size} className="text-warn" />;
    case "credentials":
      return <TerminalSquare size={size} className="text-info" />;
    case "files":
      return <Folder size={size} className="text-accent" />;
  }
  return <Folder size={size} className="text-subtle" />;
}

/** The CLI logins found on this computer, each with its logo. */
export function CredentialChips({ creds, max = 40 }: { creds: Credential[]; max?: number }) {
  // One chip per tool (gcloud, gsutil and boto all count as Google Cloud logins but read better apart).
  const seen = new Set<string>();
  const shown = creds.filter((c) => c.present && !c.skipped && !seen.has(c.label) && seen.add(c.label));
  if (shown.length === 0) return <span className="text-[11.5px] text-subtle">None found on this computer</span>;
  return (
    <div className="flex flex-wrap gap-1">
      {shown.slice(0, max).map((c) => (
        <span key={c.path} title={`~/${c.path}`} className="inline-flex h-[22px] items-center gap-1.5 rounded-md border border-line bg-panel px-1.5 text-[11px] text-muted">
          {isBrand(c.icon) ? <BrandIcon name={c.icon} size={12} className="text-fg" /> : <KeyRound size={11} className="text-subtle" />}
          {c.label}
        </span>
      ))}
      {shown.length > max && <span className="text-[11px] text-subtle">+{shown.length - max}</span>}
    </div>
  );
}
