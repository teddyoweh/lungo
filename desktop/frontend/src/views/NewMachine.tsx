import { useEffect, useState } from "react";
import { Check, Cloud, Cpu, Download, LogIn, MemoryStick, RefreshCw, Rocket } from "lucide-react";
import { api, errText, type Catalog, type ProviderStatus, type Spec } from "../lib/api";
import { loadMachines, openShellTab, runOp, setState, toast, useStore, waitOp } from "../lib/store";
import { cx, money } from "../lib/util";
import { Button, Field, Input, Modal, ModalHeader, Select, Spinner, Toggle } from "../components/ui";
import { OpLog } from "../components/OpLog";
import { BrandIcon, ProviderIcon } from "../components/Brand";


export function NewMachineDialog({ onClose }: { onClose: () => void }) {
  const [statuses, setStatuses] = useState<ProviderStatus[] | null>(null);
  const [provider, setProvider] = useState("");
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [spec, setSpec] = useState<Spec | null>(null);
  const [op, setOp] = useState<string | null>(null);
  const [connecting, setConnecting] = useState("");
  const opState = useStore((s) => (op ? s.ops[op] : undefined));
  const machines = useStore((s) => s.machines);

  const loadStatuses = async (force = false) => {
    setStatuses(null);
    const s = await api.providerStatuses(force);
    setStatuses(s);
    return s;
  };

  useEffect(() => {
    loadStatuses().then((s) => {
      const ready = s.filter((p) => p.loggedIn);
      if (ready.length === 1) choose(ready[0].id);
      else if (ready.find((p) => p.id === "gcp")) choose("gcp");
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const choose = async (id: string) => {
    setProvider(id);
    setCatalog(null);
    setSpec(null);
    try {
      const [c, filled] = await Promise.all([api.catalog(id), api.fillSpec({ provider: id })]);
      setCatalog(c);
      const st = (await api.providerStatuses()).find((p) => p.id === id);
      let account = filled.account;
      if (st && st.accounts.length && !st.accounts.some((a) => a.id === account)) {
        account = (st.accounts.find((a) => a.default) ?? st.accounts[0]).id;
      }
      const settings = await api.settings();
      setSpec({ ...filled, account, name: suggestName(machines.map((m) => m.machine.name)), tailscale: settings.tailscale, docker: settings.docker });
    } catch (e) {
      toast("error", "Couldn't load options", errText(e));
    }
  };

  const connect = async (id: string) => {
    setConnecting(id);
    try {
      const opId = await runOp(api.login(id), true);
      const end = await waitOp(opId);
      if (!end.error) {
        const s = await loadStatuses(true);
        if (s.find((p) => p.id === id)?.loggedIn) choose(id);
      }
    } finally {
      setConnecting("");
    }
  };

  const status = statuses?.find((p) => p.id === provider);
  const size = catalog?.sizes.find((s) => s.id === spec?.size);
  const monthly = (size?.monthly ?? 0) + (spec?.diskGB ?? 0) * (catalog?.diskPerGB ?? 0);
  const nameOk = !!spec && /^[a-z]([a-z0-9-]{0,40}[a-z0-9])?$/.test(spec.name) && !machines.some((m) => m.machine.name === spec.name);
  const ready = !!spec && !!status?.loggedIn && !!spec.account && !!spec.size && nameOk && (spec.region || spec.zone);

  const create = async () => {
    if (!spec) return;
    const id = await runOp(api.createMachine(spec), true);
    setOp(id);
    waitOp(id).then((end) => {
      loadMachines();
      if (!end.error) toast("success", `${spec.name} is ready`, `ssh ${spec.name}`);
    });
  };

  const set = (p: Partial<Spec>) => setSpec((s) => (s ? { ...s, ...p } : s));

  if (op) {
    const done = opState?.end && !opState.end.error;
    return (
      <Modal open onClose={onClose} width={620} dismissable={!opState?.info.running}>
        <ModalHeader
          title={done ? `${spec?.name} is ready` : opState?.end?.error ? `Couldn't finish ${spec?.name}` : `Creating ${spec?.name}`}
          subtitle={
            done
              ? `Connect any time with: ssh ${spec?.name}`
              : opState?.end?.error
                ? "The machine may still exist; check Machines."
                : "Usually 3–6 minutes. You can close this; it keeps going."
          }
          icon={done ? <Check size={15} /> : <Rocket size={15} />}
          onClose={onClose}
        />
        <div className="max-h-[58vh] overflow-y-auto px-5 py-4">
          <OpLog id={op} />
        </div>
        <div className="flex items-center justify-end gap-2 border-t border-line px-5 py-3">
          {opState?.info.running && (
            <Button variant="ghost" onClick={onClose}>
              Run in background
            </Button>
          )}
          {opState?.end?.error && <Button onClick={onClose}>Close</Button>}
          {done && (
            <>
              <Button
                variant="outline"
                onClick={() => {
                  onClose();
                  openShellTab(spec!.name);
                }}
              >
                Open shell
              </Button>
              <Button variant="primary" icon={<BrandIcon name="claude" size={14} />} onClick={() => setState({ modal: { type: "new-session", machine: spec!.name } })}>
                Start a Claude session
              </Button>
            </>
          )}
        </div>
      </Modal>
    );
  }

  return (
    <Modal open onClose={onClose} width={760}>
      <ModalHeader
        title="New machine"
        subtitle="A VM with a persistent volume for /home. Your setup syncs over when it's ready."
        icon={<Cloud size={15} />}
        onClose={onClose}
      />
      <div className="max-h-[calc(82vh-130px)] overflow-y-auto px-5 py-4">
        <Section n={1} title="Cloud">
          <div className="grid grid-cols-3 gap-2">
            {(statuses ?? [{ id: "gcp" }, { id: "aws" }, { id: "azure" }] as Partial<ProviderStatus>[]).map((p) => {
              const st = p as ProviderStatus;
              const loading = !statuses;
              const selected = provider === st.id;
              return (
                <div
                  key={st.id}
                  onClick={() => !loading && st.loggedIn && choose(st.id)}
                  className={cx(
                    "relative flex flex-col gap-2 rounded-xl border p-3 transition-colors",
                    selected ? "border-accent bg-accent-soft" : "border-line hover:border-line-strong",
                    !loading && !st.loggedIn && "opacity-90",
                  )}
                >
                  <div className="flex items-center gap-2">
                    <span className="flex size-7 items-center justify-center rounded-md border border-line bg-panel text-fg">
                      <ProviderIcon provider={st.id} size={16} />
                    </span>
                    <span className="text-[13px] font-medium text-fg">{st.label ?? { gcp: "Google Cloud", aws: "Amazon Web Services", azure: "Microsoft Azure" }[st.id!]}</span>
                    {selected && <Check size={14} className="ml-auto text-accent" />}
                  </div>
                  {loading ? (
                    <div className="flex items-center gap-1.5 text-[11.5px] text-subtle">
                      <Spinner size={11} /> Checking…
                    </div>
                  ) : st.loggedIn ? (
                    <div className="truncate text-[11.5px] text-subtle" title={st.identity}>
                      {st.identity}
                    </div>
                  ) : st.installed ? (
                    <div className="flex items-center justify-between gap-2">
                      <span className="truncate text-[11.5px] text-subtle">{st.hint || "Not connected"}</span>
                      <Button size="xs" variant="outline" icon={<LogIn size={11} />} loading={connecting === st.id} onClick={(e) => { e.stopPropagation(); connect(st.id); }}>
                        Connect
                      </Button>
                    </div>
                  ) : (
                    <div className="flex items-center justify-between gap-2">
                      <span className="truncate text-[11.5px] text-subtle">{st.cli} not installed</span>
                      <Button size="xs" variant="ghost" icon={<Download size={11} />} onClick={(e) => { e.stopPropagation(); toast("info", `Install ${st.cli}`, st.install); }}>
                        How
                      </Button>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
          <div className="mt-2 flex items-center justify-between text-[11.5px] text-subtle">
            <span>Lungo uses each cloud's own CLI login. Nothing is stored by Lungo.</span>
            <button className="flex items-center gap-1 hover:text-fg" onClick={() => loadStatuses(true)}>
              <RefreshCw size={11} /> Recheck
            </button>
          </div>
        </Section>

        {provider && !spec && (
          <div className="flex items-center gap-2 py-6 text-[12.5px] text-subtle">
            <Spinner /> Loading options…
          </div>
        )}

        {spec && catalog && status && (
          <>
            <Section n={2} title="Where">
              <div className="grid grid-cols-2 gap-3">
                <Field label={provider === "gcp" ? "Project" : provider === "aws" ? "Profile" : "Subscription"}>
                  <Select
                    value={spec.account}
                    onChange={(v) => set({ account: v })}
                    placeholder="Choose…"
                    options={status.accounts.map((a) => ({ value: a.id, label: a.label && a.label !== a.id ? `${a.label} (${a.id})` : a.id }))}
                  />
                </Field>
                <Field label="Region">
                  <Select
                    value={spec.region}
                    onChange={(v) => set({ region: v, zone: catalog.regions.find((r) => r.id === v)?.zone ?? "" })}
                    options={catalog.regions.map((r) => ({ value: r.id, label: `${r.label} · ${r.id}` }))}
                  />
                </Field>
              </div>
            </Section>

            <Section n={3} title="Size">
              <div className="grid grid-cols-3 gap-2">
                {catalog.sizes.map((s) => {
                  const sel = s.id === spec.size;
                  return (
                    <button
                      key={s.id}
                      onClick={() => set({ size: s.id })}
                      className={cx("flex flex-col gap-1.5 rounded-xl border p-3 text-left transition-colors", sel ? "border-accent bg-accent-soft" : "border-line hover:border-line-strong")}
                    >
                      <div className="flex w-full items-center justify-between">
                        <span className="text-[13px] font-semibold text-fg">{s.label}</span>
                        <span className="text-[12.5px] font-medium text-fg tabular-nums">{money(s.monthly)}</span>
                      </div>
                      <div className="flex items-center gap-3 text-[11.5px] text-muted">
                        <span className="flex items-center gap-1">
                          <Cpu size={11} /> {s.cpus} vCPU
                        </span>
                        <span className="flex items-center gap-1">
                          <MemoryStick size={11} /> {s.memoryGB} GB
                        </span>
                      </div>
                      <div className="font-mono text-[10.5px] text-subtle">{s.id}</div>
                      {s.note && <div className="text-[11px] leading-snug text-subtle">{s.note}</div>}
                    </button>
                  );
                })}
              </div>
            </Section>

            <Section n={4} title="Volume">
              <div className="flex items-center gap-4">
                <input
                  type="range"
                  min={20}
                  max={2000}
                  step={10}
                  value={spec.diskGB}
                  onChange={(e) => set({ diskGB: parseInt(e.target.value) })}
                  className="no-drag h-1 flex-1 cursor-pointer accent-[var(--accent)]"
                />
                <div className="flex items-center gap-1.5">
                  <Input type="number" min={20} max={65536} value={spec.diskGB} onChange={(e) => set({ diskGB: parseInt(e.target.value) || 20 })} className="w-24 text-right tabular-nums" />
                  <span className="text-[12.5px] text-subtle">GB</span>
                </div>
              </div>
              <div className="mt-1.5 text-[11.5px] text-subtle">
                Mounted at /home, kept when the VM is stopped or deleted with “keep volume”. ≈ {money(spec.diskGB * catalog.diskPerGB)}/mo.
              </div>
            </Section>

            <Section n={5} title="Name and setup" last>
              <div className="grid grid-cols-2 gap-3">
                <Field
                  label="Name"
                  hint={
                    spec.name && !nameOk
                      ? machines.some((m) => m.machine.name === spec.name)
                        ? "You already have a machine with this name."
                        : "Lowercase letters, digits, dashes; start with a letter."
                      : `You'll connect with: ssh ${spec.name || "name"}`
                  }
                >
                  <Input mono value={spec.name} onChange={(e) => set({ name: e.target.value.toLowerCase() })} />
                </Field>
                <Field label="User on the machine">
                  <Input mono value={spec.user} onChange={(e) => set({ user: e.target.value.toLowerCase() })} />
                </Field>
              </div>
              <div className="mt-3 grid grid-cols-2 gap-2">
                <ToggleCard title="Join my tailnet" body="Private 100.x address; SSH from anywhere." checked={spec.tailscale} onChange={(v) => set({ tailscale: v })} />
                <ToggleCard title="Install Docker" body="docker, compose and buildx." checked={spec.docker} onChange={(v) => set({ docker: v })} />
              </div>
            </Section>
          </>
        )}
      </div>
      <div className="flex items-center justify-between border-t border-line px-5 py-3">
        <div className="text-[12px] text-muted">
          {spec && size ? (
            <>
              ≈ <b className="font-semibold text-fg tabular-nums">{money(monthly)}</b>/mo while running · {money(spec.diskGB * (catalog?.diskPerGB ?? 0))}/mo stopped
            </>
          ) : (
            <span className="text-subtle">Prices are on-demand estimates.</span>
          )}
        </div>
        <div className="flex gap-2">
          <Button variant="ghost" onClick={() => setState({ modal: { type: "add-machine" } })}>
            Add existing instead
          </Button>
          <Button variant="primary" icon={<Rocket size={13} />} disabled={!ready} onClick={create}>
            Create machine
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function Section({ n, title, children, last }: { n: number; title: string; children: React.ReactNode; last?: boolean }) {
  return (
    <section className={cx("flex gap-3.5", !last && "pb-5")}>
      <div className="flex flex-col items-center">
        <div className="flex size-5 shrink-0 items-center justify-center rounded-full border border-line-strong text-[10.5px] font-semibold text-muted">{n}</div>
        {!last && <div className="mt-1 w-px flex-1 bg-line" />}
      </div>
      <div className="min-w-0 flex-1">
        <div className="mb-2 text-[13px] font-semibold text-fg">{title}</div>
        {children}
      </div>
    </section>
  );
}

function ToggleCard({ title, body, checked, onChange }: { title: string; body: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex items-center justify-between gap-3 rounded-lg border border-line px-3 py-2.5">
      <span>
        <span className="block text-[12.5px] font-medium text-fg">{title}</span>
        <span className="block text-[11.5px] text-subtle">{body}</span>
      </span>
      <Toggle checked={checked} onChange={onChange} />
    </label>
  );
}

function suggestName(taken: string[]): string {
  const base = ["dev", "box", "work", "claude"];
  for (const b of base) if (!taken.includes(b)) return b;
  for (let i = 2; i < 100; i++) if (!taken.includes(`dev-${i}`)) return `dev-${i}`;
  return "";
}
