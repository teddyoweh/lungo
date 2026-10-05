// API keys: stored in the keychain or found in shell files, put on machines as environment
// variables. Add, replace, choose machines, test against the service, stop syncing.
import { useEffect, useMemo, useState } from "react";
import { CheckCircle2, ExternalLink, Eye, EyeOff, KeyRound, MoreHorizontal, Pencil, Plus, Server, ShieldCheck, Trash2, XCircle } from "lucide-react";
import { api, errText, type APIKeyView, type APIProvider, type KeyTest } from "../lib/api";
import { getState, runOp, setState, toast, useStore } from "../lib/store";
import { cx } from "../lib/util";
import { Badge, Button, Empty, Field, GROUP, GROUP_ROW, IconButton, Input, Menu, Modal, ModalHeader, Spinner, Toggle } from "../components/ui";
import { BrandIcon, isBrand } from "../components/Brand";

const reload = () => api.apiKeys().then((k) => setState({ keys: k ?? [] }));

function KeyIcon({ icon, size = 16 }: { icon?: string; size?: number }) {
  return isBrand(icon) ? <BrandIcon name={icon} size={size} className="text-fg" /> : <KeyRound size={size - 2} className="text-subtle" />;
}

/**
 * API keys, as a section of the Accounts page: kept in your keychain, set as environment
 * variables in every shell on the machines.
 */
export function KeysSection() {
  const keys = useStore((s) => s.keys);
  const machines = useStore((s) => s.machines);
  const [providers, setProviders] = useState<APIProvider[]>([]);
  const [adding, setAdding] = useState<{ provider?: APIProvider; name?: string; machines?: string[] } | null>(null);
  const [editingMachines, setEditingMachines] = useState<APIKeyView | null>(null);
  const [tests, setTests] = useState<Record<string, KeyTest | "running">>({});

  const pending = useStore((s) => s.addKey);
  useEffect(() => {
    if (getState().keys === null) reload().catch(() => setState({ keys: [] }));
    api.apiProviders().then((p) => setProviders(p ?? [])).catch(() => {});
  }, []);
  // "Connect" on the Accounts page lands here with a service already picked.
  useEffect(() => {
    if (pending === undefined || pending === null) return;
    if (pending && providers.length === 0) return; // wait for the catalogue
    setAdding({ provider: providers.find((p) => p.id === pending) });
    setState({ addKey: null });
  }, [pending, providers]);

  const test = async (name: string) => {
    setTests((t) => ({ ...t, [name]: "running" }));
    const r = await api.testAPIKey(name).catch((e) => ({ ok: false, detail: "", error: errText(e) }) as KeyTest);
    setTests((t) => ({ ...t, [name]: r }));
  };
  const list = keys ?? [];
  const testable = list.filter((k) => k.canTest);

  return (
    <section id="keys">
      <div className="mb-2 flex h-6 items-center justify-between">
        <h2 className="text-[11px] font-semibold tracking-[0.06em] text-subtle uppercase">API keys</h2>
        <span className="flex items-center gap-1.5">
          <Button size="xs" variant="ghost" icon={<ShieldCheck size={12} />} disabled={testable.length === 0} onClick={() => testable.forEach((k) => test(k.name))}>
            Test all
          </Button>
          <Button size="xs" variant="outline" icon={<Plus size={12} />} onClick={() => setAdding({})}>
            Add key
          </Button>
        </span>
      </div>
      <div className="flex flex-col gap-3">
        {keys === null ? (
          <div className="flex justify-center p-10">
            <Spinner />
          </div>
        ) : list.length === 0 ? (
          <div className={GROUP}>
            <Empty icon={<KeyRound size={18} />} title="No API keys yet" body="Add your OpenAI, Anthropic, Gemini or any other key. Every machine gets it as an environment variable, ready for scripts and tools." />
          </div>
        ) : (
          <div className={GROUP}>
            {list.map((k) => {
              const t = tests[k.name];
              return (
                <div key={k.name} className={cx(GROUP_ROW, "grid grid-cols-[36px_minmax(200px,1.3fr)_minmax(130px,0.8fr)_minmax(150px,1fr)_auto] items-center gap-4 py-3", !k.sync && "opacity-60")}>
                  <span className="flex size-9 items-center justify-center rounded-[10px] bg-[color-mix(in_srgb,var(--fg)_6%,transparent)]">
                    <KeyIcon icon={k.icon} size={17} />
                  </span>
                  <div className="min-w-0">
                    <div className="selectable truncate font-mono text-[12.5px] font-medium text-fg">{k.name}</div>
                    <div className="flex items-center gap-1.5 truncate text-[11.5px] text-subtle">
                      {k.label !== k.name && <span>{k.label}</span>}
                      {k.stored ? <Badge tone="accent">Keychain</Badge> : <Badge tone="grey">{k.shell}</Badge>}
                      {k.stored && k.shell && <span title={`Overrides the value in ${k.shell}`}>overrides {k.shell}</span>}
                    </div>
                  </div>
                  <div className="font-mono text-[12px] text-muted">{k.masked}</div>
                  <div className="min-w-0">
                    {!k.sync ? (
                      <span className="text-[12px] text-subtle">Not on machines</span>
                    ) : k.machines.length === 0 ? (
                      <span className="flex items-center gap-1.5 text-[12px] text-muted">
                        <Server size={12} className="text-subtle" /> All machines
                      </span>
                    ) : (
                      <div className="flex flex-wrap gap-1">
                        {k.machines.map((m) => (
                          <Badge key={m} tone="grey">
                            {m}
                          </Badge>
                        ))}
                      </div>
                    )}
                    {t && t !== "running" && (
                      <div className={cx("mt-0.5 flex items-center gap-1 text-[11px]", t.ok ? "text-ok" : t.error ? "text-subtle" : "text-bad")}>
                        {t.ok ? <CheckCircle2 size={11} /> : t.error ? null : <XCircle size={11} />}
                        {t.error ?? t.detail}
                      </div>
                    )}
                  </div>
                  <div className="flex items-center gap-1.5">
                    {k.canTest && (
                      <Button size="xs" variant="outline" disabled={t === "running"} onClick={() => test(k.name)}>
                        {t === "running" ? <Spinner size={11} /> : "Test"}
                      </Button>
                    )}
                    <Toggle checked={k.sync} label={k.sync ? "On machines" : "Off"} onChange={(on) => runOp(api.setAPIKeySync(k.name, on))} />
                    <Menu
                      trigger={
                        <IconButton label="More" className="size-7">
                          <MoreHorizontal size={14} />
                        </IconButton>
                      }
                      items={[
                        { label: k.stored ? "Replace value…" : "Store in keychain…", icon: <Pencil size={13} />, onClick: () => setAdding({ name: k.name, provider: providers.find((p) => p.id === k.provider), machines: k.machines }) },
                        { label: "Choose machines…", icon: <Server size={13} />, disabled: !k.stored, hint: k.stored ? undefined : "store it first", onClick: () => setEditingMachines(k) },
                        ...(k.docs ? [{ label: `Manage at ${new URL(k.docs).hostname}`, icon: <ExternalLink size={13} />, onClick: () => api.openURL(k.docs!) }] : []),
                        "sep" as const,
                        { label: "Remove from keychain", icon: <Trash2 size={13} />, danger: true, disabled: !k.stored, onClick: () => runOp(api.removeAPIKey(k.name)) },
                      ]}
                    />
                  </div>
                </div>
              );
            })}
          </div>
        )}
        <p className="text-[11.5px] text-subtle">
          Machines load these from <code className="font-mono">~/.secrets.sh</code>. Keys exported in your shell startup files are picked up automatically; storing one here overrides
          that value and lets you pick machines. Testing sends the key only to its own service.
        </p>
      </div>

      {adding && <AddKey initial={adding} providers={providers} machines={machines.map((m) => m.machine.name)} onClose={() => setAdding(null)} />}
      {editingMachines && <MachinesPicker k={editingMachines} machines={machines.map((m) => m.machine.name)} onClose={() => setEditingMachines(null)} />}
    </section>
  );
}

const POPULAR = ["openai", "anthropic", "gemini", "openrouter", "groq", "mistral", "xai", "deepseek", "perplexity", "huggingface", "elevenlabs", "replicate"];

function AddKey({
  initial,
  providers,
  machines,
  onClose,
}: {
  initial: { provider?: APIProvider; name?: string; machines?: string[] };
  providers: APIProvider[];
  machines: string[];
  onClose: () => void;
}) {
  const [provider, setProvider] = useState<APIProvider | null>(initial.provider ?? null);
  const [name, setName] = useState(initial.name ?? initial.provider?.env ?? "");
  const [value, setValue] = useState("");
  const [show, setShow] = useState(false);
  const [all, setAll] = useState(!initial.machines || initial.machines.length === 0);
  const [picked, setPicked] = useState<string[]>(initial.machines ?? []);
  const [more, setMore] = useState(false);
  const editing = !!initial.name;
  const shown = useMemo(() => (more ? providers : providers.filter((p) => POPULAR.includes(p.id))), [providers, more]);

  const choose = (p: APIProvider | null) => {
    setProvider(p);
    if (!editing) setName(p ? p.env : "");
  };
  const save = () => {
    const n = name.trim().toUpperCase();
    if (!/^[A-Z][A-Z0-9_]{1,63}$/.test(n)) return toast("error", "Check the variable name", "Use CAPITALS, digits and _, like OPENAI_API_KEY.");
    if (!value.trim()) return toast("error", "Paste the key");
    runOp(api.setAPIKey(n, value.trim(), provider?.id ?? "", all ? null : picked));
    onClose();
  };

  return (
    <Modal open onClose={onClose} width={560}>
      <ModalHeader
        title={editing ? `Replace ${initial.name}` : "Add an API key"}
        subtitle="Saved in your keychain and set on your machines."
        onClose={onClose}
        icon={provider ? <KeyIcon icon={provider.icon} size={16} /> : <KeyRound size={15} />}
      />
      <form
        className="flex flex-col gap-4 px-5 py-4"
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        {!editing && (
          <div>
            <div className="mb-1.5 text-[12px] font-medium text-muted">Service</div>
            <div className="grid grid-cols-4 gap-1.5">
              {shown.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => choose(p)}
                  className={cx(
                    "flex items-center gap-2 rounded-lg border px-2.5 py-2 text-left text-[12px] transition-colors",
                    provider?.id === p.id ? "border-accent bg-accent-soft text-fg" : "border-line text-muted hover:border-line-strong hover:text-fg",
                  )}
                >
                  <KeyIcon icon={p.icon} size={14} />
                  <span className="truncate">{p.label}</span>
                </button>
              ))}
              <button
                type="button"
                onClick={() => choose(null)}
                className={cx("flex items-center gap-2 rounded-lg border px-2.5 py-2 text-left text-[12px]", !provider ? "border-accent bg-accent-soft text-fg" : "border-line text-muted hover:text-fg")}
              >
                <KeyRound size={13} /> Other
              </button>
              {!more && (
                <button type="button" onClick={() => setMore(true)} className="rounded-lg px-2.5 py-2 text-left text-[12px] text-accent hover:underline">
                  More services…
                </button>
              )}
            </div>
          </div>
        )}
        <div className="grid grid-cols-[1fr_1.4fr] gap-3">
          <Field label="Variable">
            <Input mono value={name} disabled={editing} onChange={(e) => setName(e.target.value.toUpperCase())} placeholder="MY_SERVICE_API_KEY" />
          </Field>
          <Field
            label={
              <span className="flex items-center justify-between">
                Key
                {provider?.docs && (
                  <button type="button" onClick={() => api.openURL(provider.docs)} className="flex items-center gap-1 text-[11px] font-normal text-accent hover:underline">
                    Get a key <ExternalLink size={10} />
                  </button>
                )}
              </span>
            }
          >
            <div className="relative">
              <Input autoFocus mono type={show ? "text" : "password"} value={value} onChange={(e) => setValue(e.target.value)} placeholder="Paste it here" className="pr-8" />
              <button type="button" onClick={() => setShow((s) => !s)} className="absolute top-1/2 right-2 -translate-y-1/2 text-subtle hover:text-fg" title={show ? "Hide" : "Show"}>
                {show ? <EyeOff size={13} /> : <Eye size={13} />}
              </button>
            </div>
          </Field>
        </div>
        <Field label="Machines">
          <div className="flex flex-col gap-2">
            <label className="flex items-center gap-2 text-[12.5px] text-muted">
              <Toggle checked={all} onChange={setAll} /> Every machine, including ones you add later
            </label>
            {!all && (
              <div className="flex flex-wrap gap-1.5">
                {machines.map((m) => {
                  const on = picked.includes(m);
                  return (
                    <button
                      key={m}
                      type="button"
                      onClick={() => setPicked((p) => (on ? p.filter((x) => x !== m) : [...p, m]))}
                      className={cx("rounded-md border px-2 py-1 text-[12px]", on ? "border-accent bg-accent-soft text-fg" : "border-line text-muted hover:text-fg")}
                    >
                      {m}
                    </button>
                  );
                })}
                {machines.length === 0 && <span className="text-[12px] text-subtle">No machines yet</span>}
              </div>
            )}
          </div>
        </Field>
        <div className="flex justify-end gap-2 pt-1">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" icon={<KeyRound size={13} />}>
            {editing ? "Replace" : "Save key"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function MachinesPicker({ k, machines, onClose }: { k: APIKeyView; machines: string[]; onClose: () => void }) {
  const [all, setAll] = useState(k.machines.length === 0);
  const [picked, setPicked] = useState<string[]>(k.machines);
  return (
    <Modal open onClose={onClose} width={440}>
      <ModalHeader title={`Machines for ${k.name}`} onClose={onClose} icon={<KeyIcon icon={k.icon} size={16} />} />
      <div className="flex flex-col gap-3 px-5 py-4">
        <label className="flex items-center gap-2 text-[12.5px] text-muted">
          <Toggle checked={all} onChange={setAll} /> Every machine
        </label>
        {!all && (
          <div className="flex flex-wrap gap-1.5">
            {machines.map((m) => {
              const on = picked.includes(m);
              return (
                <button
                  key={m}
                  type="button"
                  onClick={() => setPicked((p) => (on ? p.filter((x) => x !== m) : [...p, m]))}
                  className={cx("rounded-md border px-2 py-1 text-[12px]", on ? "border-accent bg-accent-soft text-fg" : "border-line text-muted hover:text-fg")}
                >
                  {m}
                </button>
              );
            })}
          </div>
        )}
        <div className="flex justify-end gap-2 pt-1">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            onClick={() => {
              runOp(api.setAPIKeyMachines(k.name, all ? null : picked));
              onClose();
            }}
          >
            Save
          </Button>
        </div>
      </div>
    </Modal>
  );
}
