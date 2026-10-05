// Accounts: everything machines sign in with, as plain rows. Claude accounts (with usage and
// switching), clouds, the tailnet, API keys and CLI logins, plus one "Connect" picker that
// adds any of them.
import { useEffect, useMemo, useState } from "react";
import { FolderOpen, KeyRound, Plus, RefreshCw, Search, SquareTerminal } from "lucide-react";
import { api, errText, type APIProvider, type Credential, type ProviderStatus, type TailscaleInfo } from "../lib/api";
import { getState, runOp, setState, toast, useStore, waitOp } from "../lib/store";
import { cx } from "../lib/util";
import { Button, CopyField, Field, Input, Modal, ModalHeader, Page, Spinner, GROUP, GROUP_ROW } from "../components/ui";
import { BrandIcon, isBrand, ProviderIcon } from "../components/Brand";
import { AddClaudeAccount, ClaudeAccountsSection } from "../components/ClaudeAccounts";
import { KeysSection } from "./Keys";

const cloudNames: Record<string, string> = { gcp: "Google Cloud", aws: "Amazon Web Services", azure: "Microsoft Azure" };
const firstURL = (s?: string) => s?.match(/https?:\/\/\S+/)?.[0];

function SectionTitle({ children, right }: { children: React.ReactNode; right?: React.ReactNode }) {
  return (
    <div className="mb-2 flex h-6 items-center justify-between">
      <h2 className="text-[11px] font-semibold tracking-[0.06em] text-subtle uppercase">{children}</h2>
      {right}
    </div>
  );
}

function StatusPill({ tone, children }: { tone: "ok" | "warn" | "off"; children: React.ReactNode }) {
  const cls = { ok: "text-ok", warn: "text-warn", off: "text-subtle" }[tone];
  const dot = { ok: "bg-ok", warn: "bg-warn", off: "bg-subtle" }[tone];
  return (
    <span className={cx("inline-flex items-center gap-1.5 text-[12px] font-medium whitespace-nowrap", cls)}>
      <span className={cx("size-1.5 rounded-full", dot)} />
      {children}
    </span>
  );
}

/** One connection: logo, name, a line of detail, its state and an action. */
function Row({ icon, title, detail, status, action, onClick }: { icon: React.ReactNode; title: React.ReactNode; detail?: React.ReactNode; status?: React.ReactNode; action?: React.ReactNode; onClick?: () => void }) {
  return (
    <div onClick={onClick} className={cx(GROUP_ROW, "flex items-center gap-3.5 py-3", onClick && "cursor-pointer hover:bg-hover")}>
      <span className="flex size-9 shrink-0 items-center justify-center rounded-[10px] bg-[color-mix(in_srgb,var(--fg)_6%,transparent)] text-fg">{icon}</span>
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] font-semibold text-fg">{title}</div>
        {detail && <div className="truncate text-[11.5px] text-subtle">{detail}</div>}
      </div>
      {status}
      <div className="flex w-[128px] shrink-0 justify-end">{action}</div>
    </div>
  );
}

export function AccountsView() {
  const info = useStore((s) => s.info);
  const [statuses, setStatuses] = useState<ProviderStatus[] | null>(null);
  const [ts, setTs] = useState<TailscaleInfo | null>(null);
  const [creds, setCreds] = useState<Credential[] | null>(null);
  const [checking, setChecking] = useState(false);
  const [picker, setPicker] = useState(false);
  const [addClaude, setAddClaude] = useState(false);
  const [tsOpen, setTsOpen] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);

  const load = async (force = false) => {
    setChecking(true);
    try {
      const [s, t] = await Promise.all([api.providerStatuses(force), api.tailscale()]);
      setStatuses(s);
      setTs(t);
    } finally {
      setChecking(false);
    }
  };
  useEffect(() => {
    load();
    api.credentials().then((c) => setCreds(c ?? [])).catch(() => setCreds([]));
    if (getState().keys === null) api.apiKeys().then((k) => setState({ keys: k ?? [] })).catch(() => setState({ keys: [] }));
  }, []);

  const connectCloud = async (p: ProviderStatus) => {
    if (!p.installed) {
      const url = firstURL(p.install);
      if (url) api.openURL(url);
      return;
    }
    setBusy(p.id);
    try {
      await waitOp(await runOp(api.login(p.id)));
      await load(true);
    } finally {
      setBusy(null);
    }
  };

  const tools = useMemo(() => {
    const m = new Map<string, Credential>();
    for (const c of creds ?? []) if (c.present && !c.skipped && !m.has(c.label)) m.set(c.label, c);
    return [...m.values()];
  }, [creds]);

  return (
    <Page
      title="Accounts"
      subtitle="Everything your machines sign in with."
      actions={
        <>
          <Button variant="ghost" icon={<RefreshCw size={13} className={checking ? "spin" : ""} />} onClick={() => load(true)}>
            Recheck
          </Button>
          <Button variant="primary" icon={<Plus size={13} />} onClick={() => setPicker(true)}>
            Connect
          </Button>
        </>
      }
    >
      <div className="mx-auto flex max-w-[940px] flex-col gap-7 px-6 py-6">
        <ClaudeAccountsSection />

        <section>
          <SectionTitle>Clouds</SectionTitle>
          <div className={GROUP}>
            {(statuses ?? [null, null, null]).map((p, i) =>
              !p ? (
                <div key={i} className={cx(GROUP_ROW, "flex h-[62px] items-center justify-center")}>
                  <Spinner size={13} />
                </div>
              ) : (
                <Row
                  key={p.id}
                  icon={<ProviderIcon provider={p.id} size={19} />}
                  title={cloudNames[p.id] ?? p.label}
                  detail={
                    p.loggedIn
                      ? `${p.identity} · ${p.accounts.length} ${p.id === "gcp" ? "project" : p.id === "aws" ? "profile" : "subscription"}${p.accounts.length === 1 ? "" : "s"}`
                      : p.installed
                        ? "Signed out. Connecting opens the sign-in in your browser."
                        : `Needs the ${p.cli} command-line tool on this computer`
                  }
                  status={p.loggedIn ? <StatusPill tone="ok">Connected</StatusPill> : p.installed ? <StatusPill tone="warn">Signed out</StatusPill> : <StatusPill tone="off">Not set up</StatusPill>}
                  action={
                    <Button size="xs" variant={p.loggedIn ? "ghost" : "outline"} loading={busy === p.id} onClick={() => connectCloud(p)}>
                      {p.loggedIn ? "Sign in again" : p.installed ? "Connect" : "Get the CLI"}
                    </Button>
                  }
                />
              ),
            )}
          </div>
        </section>

        <section>
          <SectionTitle>Network</SectionTitle>
          <div className={GROUP}>
            <Row
              icon={<BrandIcon name="tailscale" size={18} />}
              title="Tailscale"
              detail={
                !ts
                  ? "Checking…"
                  : ts.connected
                    ? `${ts.self} · ${ts.tailnet}${ts.hasAuthKey ? " · new machines join on their own" : " · you approve each new machine in the browser"}`
                    : ts.installed
                      ? `Not connected (${ts.state || "stopped"}). Machines stay on their public address.`
                      : "Not installed. Optional: private addresses and SSH from anywhere."
              }
              status={!ts ? null : ts.connected ? <StatusPill tone="ok">Connected</StatusPill> : ts.installed ? <StatusPill tone="warn">Off</StatusPill> : <StatusPill tone="off">Not set up</StatusPill>}
              action={
                ts?.installed ? (
                  <Button size="xs" variant="ghost" onClick={() => setTsOpen(true)}>
                    {ts.hasAuthKey ? "Auth key saved" : "Add auth key"}
                  </Button>
                ) : (
                  <Button size="xs" variant="outline" onClick={() => api.openURL("https://tailscale.com/download")}>
                    Get Tailscale
                  </Button>
                )
              }
            />
          </div>
        </section>

        <KeysSection />

        <section>
          <SectionTitle>Logins</SectionTitle>
          <div className={GROUP}>
            <div className={cx(GROUP_ROW, "flex items-start gap-3.5 py-3")}>
              <span className="flex size-9 shrink-0 items-center justify-center rounded-[10px] bg-[color-mix(in_srgb,var(--fg)_6%,transparent)]">
                <SquareTerminal size={16} className="text-info" />
              </span>
              <div className="min-w-0 flex-1">
                <div className="text-[13px] font-semibold text-fg">CLI logins</div>
                <div className="mb-2 text-[11.5px] text-subtle">
                  {creds === null ? "Checking…" : `${tools.length + 2} found on this computer. Sync copies them to machines; choose which on the Sync page.`}
                </div>
                <div className="flex flex-wrap gap-1">
                  {[{ label: "GitHub", icon: "github", path: "gh" }, { label: "Git", icon: "git", path: ".gitconfig" }, ...tools].map((c) => (
                    <span key={c.label} title={c.path === "gh" ? "every gh account" : `~/${c.path}`} className="inline-flex h-[24px] items-center gap-1.5 rounded-md bg-panel px-2 text-[11.5px] text-muted">
                      {isBrand(c.icon) ? <BrandIcon name={c.icon} size={12} className="text-fg" /> : <KeyRound size={11} className="text-subtle" />}
                      {c.label}
                    </span>
                  ))}
                </div>
              </div>
            </div>
          </div>
        </section>

        <div className="flex items-center gap-3 text-[11.5px] text-subtle">
          <span className="min-w-0 flex-1">
            Settings live in <code className="selectable font-mono text-muted">{info?.configDir}</code>, shared with the <code className="font-mono text-muted">sky</code> command. Tokens and keys stay in your system keychain.
          </span>
          <button onClick={() => info && api.reveal(info.configDir)} className="flex shrink-0 items-center gap-1 hover:text-fg">
            <FolderOpen size={12} /> Open folder
          </button>
        </div>
      </div>

      {picker && (
        <ConnectPicker
          statuses={statuses ?? []}
          onClose={() => setPicker(false)}
          onClaude={() => setAddClaude(true)}
          onCloud={(id) => {
            const p = statuses?.find((s) => s.id === id);
            if (p) connectCloud(p);
          }}
          onTailscale={() => setTsOpen(true)}
        />
      )}
      <AddClaudeAccount open={addClaude} onClose={() => setAddClaude(false)} />
      {tsOpen && ts && <TailscaleDialog t={ts} onClose={() => setTsOpen(false)} onChange={() => api.tailscale().then(setTs)} />}
    </Page>
  );
}

/** One place to add anything: a Claude account, a cloud, the tailnet, or an API key. */
function ConnectPicker({ statuses, onClose, onClaude, onCloud, onTailscale }: { statuses: ProviderStatus[]; onClose: () => void; onClaude: () => void; onCloud: (id: string) => void; onTailscale: () => void }) {
  const [providers, setProviders] = useState<APIProvider[]>([]);
  const [q, setQ] = useState("");
  useEffect(() => {
    api.apiProviders().then((p) => setProviders(p ?? [])).catch(() => {});
  }, []);
  type Item = { key: string; label: string; hint: string; icon: React.ReactNode; group: string; run: () => void };
  const pick = (run: () => void) => () => {
    onClose();
    run();
  };
  const items: Item[] = [
    { key: "claude", label: "Claude account", hint: "Subscription for machines", icon: <BrandIcon name="claude" size={18} />, group: "Accounts", run: pick(onClaude) },
    ...(["gcp", "aws", "azure"] as const).map((id) => ({
      key: id,
      label: cloudNames[id],
      hint: statuses.find((s) => s.id === id)?.loggedIn ? "Connected · sign in again" : "Create machines here",
      icon: <ProviderIcon provider={id} size={18} />,
      group: "Accounts",
      run: pick(() => onCloud(id)),
    })),
    { key: "tailscale", label: "Tailscale auth key", hint: "Machines join on their own", icon: <BrandIcon name="tailscale" size={17} className="text-fg" />, group: "Accounts", run: pick(onTailscale) },
    ...providers.map((p) => ({
      key: `key-${p.id}`,
      label: p.label,
      hint: p.env,
      icon: isBrand(p.icon) ? <BrandIcon name={p.icon} size={17} className="text-fg" /> : <KeyRound size={15} className="text-subtle" />,
      group: "API keys",
      run: pick(() => setState({ view: "accounts", addKey: p.id })),
    })),
    { key: "key-other", label: "Another API key", hint: "Any NAME=value", icon: <KeyRound size={15} className="text-subtle" />, group: "API keys", run: pick(() => setState({ view: "accounts", addKey: "" })) },
  ];
  const needle = q.trim().toLowerCase();
  const shown = needle ? items.filter((i) => `${i.label} ${i.hint}`.toLowerCase().includes(needle)) : items;
  const groups = ["Accounts", "API keys"].map((g) => ({ g, list: shown.filter((i) => i.group === g) })).filter((x) => x.list.length);
  return (
    <Modal open onClose={onClose} width={620}>
      <div className="flex items-center gap-2.5 px-4 pt-3.5 pb-2">
        <Search size={15} className="text-subtle" />
        <input
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && shown[0]) shown[0].run();
          }}
          placeholder="What do you want to connect? Claude, AWS, OpenAI…"
          className="h-7 min-w-0 flex-1 bg-transparent text-[14px] text-fg outline-none placeholder:text-subtle"
        />
      </div>
      <div className="max-h-[440px] overflow-y-auto px-3 py-3">
        {groups.length === 0 && <div className="px-2 py-6 text-center text-[12.5px] text-subtle">Nothing matches. Choose “Another API key” to add any key by name.</div>}
        {groups.map(({ g, list }) => (
          <div key={g} className="mb-3 last:mb-0">
            <div className="mb-1.5 px-1 text-[11px] font-semibold tracking-[0.06em] text-subtle uppercase">{g}</div>
            <div className="grid grid-cols-3 gap-1.5">
              {list.map((i) => (
                <button key={i.key} onClick={i.run} className="flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-left transition-colors hover:bg-hover">
                  <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-[color-mix(in_srgb,var(--fg)_6%,transparent)]">{i.icon}</span>
                  <span className="min-w-0">
                    <span className="block truncate text-[12.5px] font-medium text-fg">{i.label}</span>
                    <span className="block truncate text-[11px] text-subtle">{i.hint}</span>
                  </span>
                </button>
              ))}
            </div>
          </div>
        ))}
      </div>
    </Modal>
  );
}

function TailscaleDialog({ t, onClose, onChange }: { t: TailscaleInfo; onClose: () => void; onChange: () => void }) {
  const [key, setKey] = useState("");
  const save = (k: string) =>
    api
      .setTailscaleKey(k)
      .then(() => {
        toast(k ? "success" : "info", k ? "Auth key saved" : "Auth key removed");
        onChange();
        onClose();
      })
      .catch((e) => toast("error", "Couldn't save key", errText(e)));
  return (
    <Modal open onClose={onClose} width={480}>
      <ModalHeader title="Tailscale" subtitle="Private addresses for every machine; SSH from anywhere without open ports." onClose={onClose} icon={<BrandIcon name="tailscale" size={16} className="text-fg" />} />
      <div className="flex flex-col gap-4 px-5 py-4">
        {t.self && <CopyField value={`${t.self}${t.ip ? ` · ${t.ip}` : ""}`} label="This device" />}
        <Field
          label="Auth key"
          hint={
            <>
              With a reusable key, new machines join your tailnet on their own. Without one you approve each machine in the browser.{" "}
              <button type="button" className="text-accent hover:underline" onClick={() => api.openURL("https://login.tailscale.com/admin/settings/keys")}>
                Create a key
              </button>
            </>
          }
        >
          <Input autoFocus type="password" mono value={key} onChange={(e) => setKey(e.target.value)} placeholder={t.hasAuthKey ? "A key is saved. Paste a new one to replace it." : "tskey-auth-…"} />
        </Field>
        <div className="flex justify-end gap-2">
          {t.hasAuthKey && (
            <Button variant="ghost" onClick={() => save("")}>
              Remove saved key
            </Button>
          )}
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!key} onClick={() => save(key)}>
            Save
          </Button>
        </div>
      </div>
    </Modal>
  );
}
