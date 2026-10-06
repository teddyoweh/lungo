import { useEffect, useMemo, useState } from "react";
import { Check } from "lucide-react";
import { api, errText, type LocalTmuxInfo, type Settings } from "../lib/api";
import { isDark, openWelcome, pickSkin, restartToUpdate, runOp, setState, setTermPrefs, setTheme, setZoom, termFontSize, toast, useStore, waitOp, ZOOM_MAX, ZOOM_MIN, type TermPrefs, type Theme } from "../lib/store";
import { APP_ICONS } from "../lib/appicons";
import { THEMES, type AppTheme } from "../lib/themes";
import { cx, mod } from "../lib/util";
import { Button, Card, Checkbox, Field, Input, Page, Segmented, Select, Spinner, Textarea, Toggle } from "../components/ui";

export function SettingsView() {
  const info = useStore((s) => s.info);
  const term = useStore((s) => s.term);
  const zoom = useStore((s) => s.zoom);
  const [saved, setSaved] = useState<Settings | null>(null);
  const [s, setS] = useState<Settings | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.settings().then((v) => {
      setSaved(v);
      setS(v);
    });
  }, []);

  const dirty = useMemo(() => JSON.stringify(s) !== JSON.stringify(saved), [s, saved]);
  if (!s || !info) {
    return (
      <Page title="Settings">
        <div className="flex justify-center py-16">
          <Spinner />
        </div>
      </Page>
    );
  }
  const set = (p: Partial<Settings>) => setS({ ...s, ...p });
  const save = async () => {
    setBusy(true);
    try {
      await api.saveSettings(s);
      setSaved(s);
      toast("success", "Settings saved");
    } catch (e) {
      toast("error", "Couldn't save settings", errText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Page
      title="Settings"
      actions={
        <>
          {dirty && (
            <Button variant="ghost" onClick={() => setS(saved)}>
              Discard
            </Button>
          )}
          <Button variant="primary" icon={<Check size={13} />} disabled={!dirty} loading={busy} onClick={save}>
            Save
          </Button>
        </>
      }
    >
      <div className="mx-auto flex max-w-[760px] flex-col gap-6 px-6 py-5">
        <Appearance />

        <Group title="Terminal">
          <Row label="Text size" hint={`The same as zooming: ${mod}+ and ${mod}− anywhere, ${mod}0 to reset.`}>
            <div className="flex items-center gap-1.5">
              <Button size="xs" variant="outline" disabled={zoom <= ZOOM_MIN} onClick={() => setZoom(zoom - 1)}>
                −
              </Button>
              <span className="w-12 text-center text-[12.5px] text-fg tabular-nums">{termFontSize(zoom)} px</span>
              <Button size="xs" variant="outline" disabled={zoom >= ZOOM_MAX} onClick={() => setZoom(zoom + 1)}>
                +
              </Button>
            </div>
          </Row>
          <Row label="Line height" hint="Compact fits more rows; box drawings join either way.">
            <Segmented<TermPrefs["line"]>
              value={term.line}
              onChange={(line) => setTermPrefs({ line })}
              options={[
                { value: "compact", label: "Compact" },
                { value: "normal", label: "Normal" },
              ]}
            />
          </Row>
          <Row label="Cursor">
            <Segmented<TermPrefs["cursor"]>
              value={term.cursor}
              onChange={(cursor) => setTermPrefs({ cursor })}
              options={[
                { value: "block", label: "Block" },
                { value: "bar", label: "Bar" },
                { value: "underline", label: "Underline" },
              ]}
            />
          </Row>
          <Row label="Blinking cursor">
            <Toggle checked={term.blink} onChange={(blink) => setTermPrefs({ blink })} />
          </Row>
          <PreciseScroll />
          <Row label="Folder and branch under each pane" hint="Click the folder to copy its path or open a terminal there; click the branch to move the repository to a machine or back.">
            <Toggle checked={term.footer} onChange={(footer) => setTermPrefs({ footer })} />
          </Row>
        </Group>

        <Continuity />
        <Updates />

        <Group title="Welcome">
          <Row label="The welcome" hint="What Lungo is and how it works, in a few short stories. Also in Help ▸ Welcome to Lungo.">
            <Button size="sm" variant="outline" onClick={openWelcome}>
              Show it again
            </Button>
          </Row>
        </Group>

        <Group title="New machines">
          <Row label="Username on machines" hint={`Created on every new machine. Defaults to ${info.localUser}.`}>
            <Input mono className="w-48" value={s.remoteUser} onChange={(e) => set({ remoteUser: e.target.value.toLowerCase() })} />
          </Row>
          <Row label="Default cloud">
            <Select
              className="w-48"
              value={s.defaultProvider || "gcp"}
              onChange={(v) => set({ defaultProvider: v })}
              options={info.providers.map((p) => ({ value: p.id, label: p.label }))}
            />
          </Row>
          <Row label="Join my tailnet" hint="New machines get a private 100.x address.">
            <Toggle checked={s.tailscale} onChange={(v) => set({ tailscale: v })} />
          </Row>
          <Row label="Install Docker">
            <Toggle checked={s.docker} onChange={(v) => set({ docker: v })} />
          </Row>
          <Row label="Land SSH logins in tmux" hint="So sessions survive disconnects. Set SKY_NO_TMUX=1 to skip once.">
            <Toggle checked={s.autoTmux} onChange={(v) => set({ autoTmux: v })} />
          </Row>
        </Group>

        <Group title="Connecting">
          <Row label="Terminal app" hint={`Used by “Open in Terminal app”.${info.terminals.includes("Warp") ? " Warp gets the command through a launch configuration." : ""}`}>
            <Select
              className="w-48"
              value={s.terminal || info.terminals[0] || ""}
              onChange={(v) => set({ terminal: v })}
              options={info.terminals.map((t) => ({ value: t, label: t }))}
            />
          </Row>
        </Group>

        <Group title="Sync">
          <Row label="Background sync" hint="How often the sky agent syncs (`sky agent install`).">
            <Select
              className="w-48"
              value={String(s.syncInterval || 120)}
              onChange={(v) => set({ syncInterval: parseInt(v) })}
              options={[60, 120, 300, 600, 1800, 3600].map((n) => ({ value: String(n), label: n < 3600 ? `Every ${n / 60} min` : "Every hour" }))}
            />
          </Row>
          <div className="px-4 py-3">
            <Field label="Extra files and folders" hint="One per line, relative to your home folder. Synced by the “Extra files” item.">
              <Textarea
                rows={3}
                className="font-mono text-[12px]"
                value={s.syncPaths.join("\n")}
                onChange={(e) => set({ syncPaths: e.target.value.split("\n") })}
                placeholder={".config/zed/settings.json\n.ripgreprc"}
              />
            </Field>
          </div>
          <div className="px-4 py-3">
            <div className="mb-2 text-[12px] font-medium text-muted">CLI logins to copy</div>
            <div className="grid grid-cols-3 gap-x-4 gap-y-2">
              {info.credentialNames.map((c) => (
                <Checkbox
                  key={c}
                  checked={!s.skipCredentials.includes(c)}
                  onChange={(v) => set({ skipCredentials: v ? s.skipCredentials.filter((x) => x !== c) : [...s.skipCredentials, c] })}
                  label={<span className="font-mono text-[11.5px] text-muted">~/{c}</span>}
                />
              ))}
            </div>
          </div>
          <div className="px-4 py-3">
            <Field label="MCP servers to leave out" hint="Comma-separated names. Servers on localhost are always left out.">
              <Input mono value={s.skipMCP.join(", ")} onChange={(e) => set({ skipMCP: e.target.value.split(",").map((x) => x.trim()) })} />
            </Field>
          </div>
        </Group>

        <div className="pb-6 text-center text-[11.5px] text-subtle">
          Lungo {info.version} · {info.platform} · settings are shared with the sky CLI
        </div>
      </div>
    </Page>
  );
}

/** A theme as a small picture of itself: its background, text, accent and six terminal colours. */
/**
 * Appearance: the mode, then the dark themes and the light themes. One of each is kept: the
 * mode says which is on screen (System follows macOS).
 */
function Appearance() {
  const theme = useStore((s) => s.theme);
  const skins = useStore((s) => s.skins);
  const showing = theme === "system" ? (isDark() ? "dark" : "light") : theme;
  const row = (dark: boolean) => (
    <div>
      <div className="mb-2 flex items-baseline gap-2">
        <span className={cx("text-[11px] font-semibold tracking-[0.06em] uppercase", showing === (dark ? "dark" : "light") ? "text-muted" : "text-subtle")}>{dark ? "Dark" : "Light"}</span>
        {theme === "system" && <span className="text-[11px] text-subtle">{dark ? "at night" : "by day"}</span>}
      </div>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(132px,1fr))] gap-x-3 gap-y-3.5">
        {THEMES.filter((t) => t.dark === dark).map((t) => (
          <ThemeTile key={t.id} theme={t} chosen={(dark ? skins.dark : skins.light) === t.id} onPick={() => pickSkin(t.id)} />
        ))}
      </div>
    </div>
  );
  return (
    <section>
      <div className="mb-3 flex items-center justify-between gap-4">
        <h2 className="text-[13px] font-semibold text-fg">Appearance</h2>
        <Segmented<Theme>
          value={theme}
          onChange={setTheme}
          options={[
            { value: "system", label: "System" },
            { value: "dark", label: "Dark" },
            { value: "light", label: "Light" },
          ]}
        />
      </div>
      <div className="flex flex-col gap-5">
        {row(true)}
        {row(false)}
        <AppIconPicker />
      </div>
    </section>
  );
}

/**
 * The app's icon. On macOS it changes in the Dock at once, in every window, and stays after
 * quitting; on Linux it applies at the next launch. Windows takes the icon from the program.
 */
function AppIconPicker() {
  const info = useStore((s) => s.info);
  if (!info || info.platform === "windows") return null;
  const pick = (id: string) => {
    if (id === info.appIcon) return;
    const was = info.appIcon;
    setState({ info: { ...info, appIcon: id } });
    api.setAppIcon(id).catch((e) => {
      setState((s) => (s.info ? { info: { ...s.info, appIcon: was } } : {}));
      toast("error", "Couldn't change the icon", errText(e));
    });
  };
  return (
    <div>
      <div className="mb-2 flex items-baseline gap-2">
        <span className="text-[11px] font-semibold tracking-[0.06em] text-muted uppercase">App icon</span>
        <span className="text-[11px] text-subtle">{info.platform === "darwin" ? "in the Dock and Finder" : "from the next launch"}</span>
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-3">
        {APP_ICONS.map((i) => {
          const chosen = info.appIcon === i.id;
          return (
            <button key={i.id} onClick={() => pick(i.id)} title={i.name} className="group flex flex-col items-center gap-1.5">
              {/* The pictures carry the system's margin and shadow around the tile; this
                  crops to the tile so the ring sits on its edge. */}
              <span
                className={cx(
                  "relative block h-16 w-16 overflow-hidden rounded-[14.4px] transition-shadow duration-100",
                  chosen ? "shadow-[0_0_0_2px_var(--bg),0_0_0_4px_var(--accent)]" : "group-hover:shadow-[0_0_0_2px_var(--bg),0_0_0_3px_var(--line-strong)]",
                )}
              >
                <img src={i.src} alt="" draggable={false} className="absolute -top-2 -left-2 h-20 w-20 max-w-none" />
              </span>
              <span className={cx("text-[11.5px]", chosen ? "font-medium text-fg" : "text-muted group-hover:text-fg")}>{i.name}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

/** A theme as a small window: its sidebar, a prompt and a few lines in its own colours. */
export function ThemeTile({ theme, chosen, onPick }: { theme: AppTheme; chosen: boolean; onPick: () => void }) {
  const c = theme.chrome;
  const t = theme.term;
  return (
    <button onClick={onPick} title={theme.name} className="group flex flex-col gap-1.5 text-left">
      <div
        className={cx("w-full overflow-hidden rounded-lg transition-shadow duration-100", chosen ? "shadow-[0_0_0_2px_var(--accent)]" : "group-hover:shadow-[0_0_0_1px_var(--line-strong)]")}
        style={{ background: t.background, boxShadow: chosen ? undefined : `inset 0 0 0 1px rgba(${c.ink}, 0.1)` }}
      >
        <div className="flex h-[60px]">
          <div className="flex w-[22%] flex-col gap-[3px] p-[7px]" style={{ background: c.sidebar }}>
            <div className="h-[3px] w-full rounded-full" style={{ background: c.fg, opacity: 0.75 }} />
            <div className="h-[3px] w-2/3 rounded-full" style={{ background: c.subtle, opacity: 0.8 }} />
            <div className="h-[3px] w-2/3 rounded-full" style={{ background: c.subtle, opacity: 0.8 }} />
          </div>
          <div className="flex min-w-0 flex-1 flex-col gap-[4px] p-[7px] font-mono text-[7.5px] leading-none">
            <div className="truncate" style={{ color: t.foreground }}>
              <span style={{ color: t.blue }}>~/api</span> <span style={{ color: t.magenta }}>main</span> <span style={{ color: c.subtle }}>%</span>
            </div>
            <div className="truncate" style={{ color: t.green }}>✓ 42 passed</div>
            <div className="truncate" style={{ color: t.yellow }}>
              warn <span style={{ color: t.foreground, opacity: 0.7 }}>unused import</span>
            </div>
            <div className="mt-auto h-[3px] w-1/3 rounded-full" style={{ background: c.accent }} />
          </div>
        </div>
      </div>
      <span className={cx("truncate px-0.5 text-[11.5px]", chosen ? "font-medium text-fg" : "text-muted group-hover:text-fg")}>{theme.name}</span>
    </button>
  );
}

/**
 * Claude Code speeds its own scrolling up when wheel steps come fast, which makes a trackpad
 * overshoot here. This turns that off in Claude's settings, so a line of travel is a line.
 */
function PreciseScroll() {
  const [on, setOn] = useState<boolean | null>(null);
  useEffect(() => {
    api.claudePreciseScroll().then(setOn, () => setOn(false));
  }, []);
  return (
    <Row label="Precise scrolling in Claude" hint="Scrolling follows the trackpad line for line instead of speeding up and jumping. Sets wheelScrollAccelerationEnabled in ~/.claude/settings.json (synced to machines). Running sessions pick it up when Claude restarts: right-click a tab, Restart Claude.">
      <Toggle
        checked={!!on}
        onChange={(v) => {
          setOn(v);
          api.setClaudePreciseScroll(v).then(
            () => api.sync([], ["claude"]).catch(() => {}),
            (e) => {
              setOn(!v);
              toast("error", "Couldn't change Claude's settings", errText(e));
            },
          );
        }}
      />
    </Row>
  );
}

/** Picking up where you left off: sessions on this computer, and opening at login. */
function Continuity() {
  const [tmux, setTmux] = useState<LocalTmuxInfo | null>(null);
  const [login, setLogin] = useState(false);
  const [installing, setInstalling] = useState(false);
  useEffect(() => {
    api.localTmux().then(setTmux, () => {});
    api.openAtLogin().then(setLogin, () => {});
  }, []);
  const install = async () => {
    setInstalling(true);
    try {
      const end = await waitOp(await runOp(api.installTmux(), true));
      const now = await api.localTmux();
      setTmux(now);
      setState({ localTmux: now.installed });
      if (!end.error && now.installed) toast("success", "tmux installed", "New terminals on this computer keep running when Lungo quits.");
    } catch {
      /* runOp said why */
    }
    setInstalling(false);
  };
  return (
    <Group title="Picking up where you left off">
      <Row
        label="Sessions on this computer"
        hint={
          !tmux
            ? undefined
            : tmux.installed
              ? "They run in tmux, like the ones on machines: quit Lungo or restart it and they are still there. After a reboot Claude resumes its conversations."
              : `They end when Lungo quits. With tmux they keep running${tmux.canInstall ? "" : ` (install it with: ${tmux.how})`}.`
        }
      >
        {!tmux ? (
          <Spinner />
        ) : tmux.installed ? (
          <span className="flex items-center gap-1.5 text-[12px] text-muted">
            <Check size={13} className="text-ok" /> Kept running
          </span>
        ) : tmux.canInstall ? (
          <Button size="sm" variant="outline" loading={installing} onClick={install}>
            Install tmux
          </Button>
        ) : (
          <span className="text-[12px] text-subtle">tmux not installed</span>
        )}
      </Row>
      <Row label="Open Lungo when I log in" hint="Every tab comes back attached to its session.">
        <Toggle
          checked={login}
          onChange={(on) => {
            setLogin(on);
            api.setOpenAtLogin(on).catch((e) => {
              setLogin(!on);
              toast("error", "Couldn't change that", errText(e));
            });
          }}
        />
      </Row>
    </Group>
  );
}

/** This version, the newest, and whether Lungo keeps itself up to date. */
function Updates() {
  const u = useStore((s) => s.update);
  const [checking, setChecking] = useState(false);
  if (!u) return null;
  const check = async () => {
    setChecking(true);
    try {
      const now = await api.checkForUpdates();
      setState({ update: now });
      if (now.state === "" && now.latest) toast("info", "Lungo is up to date", `${now.current} is the newest version.`);
      if (now.state === "error") toast("error", "Couldn't check for updates", now.error);
    } catch (e) {
      toast("error", "Couldn't check for updates", errText(e));
    }
    setChecking(false);
  };
  const status =
    u.state === "off"
      ? u.why
      : u.state === "ready"
        ? `${u.latest} is downloaded. It goes in when Lungo restarts; sessions keep running and every window comes back.`
        : u.state === "downloading"
          ? `Downloading ${u.latest}${u.progress ? ` · ${Math.round(u.progress * 100)}%` : ""}`
          : u.state === "checking"
            ? "Checking…"
            : u.state === "error"
              ? u.error
              : u.checkedAt
                ? `Up to date · checked ${new Date(u.checkedAt).toLocaleString([], { weekday: "short", hour: "numeric", minute: "2-digit" })}`
                : "Up to date";
  return (
    <Group title="Updates">
      <Row label={`Lungo ${u.current}`} hint={status}>
        {u.state === "ready" ? (
          <Button size="sm" variant="primary" onClick={restartToUpdate}>
            Restart to update
          </Button>
        ) : (
          <Button size="sm" variant="outline" loading={checking || u.state === "checking" || u.state === "downloading"} disabled={u.state === "off"} onClick={check}>
            Check now
          </Button>
        )}
      </Row>
      {u.state !== "off" && (
        <Row label="Update automatically" hint="New versions download in the background and go in when Lungo next restarts or quits.">
          <Toggle
            checked={u.auto}
            onChange={(on) => {
              setState({ update: { ...u, auto: on } });
              api.setAutoUpdate(on).catch((e) => toast("error", "Couldn't change that", errText(e)));
            }}
          />
        </Row>
      )}
    </Group>
  );
}

function Group({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section>
      <h2 className="mb-2 text-[13px] font-semibold text-fg">{title}</h2>
      <Card className="divide-y divide-[var(--line)]">{children}</Card>
    </section>
  );
}

function Row({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-4 px-4 py-3">
      <div className="min-w-0 flex-1">
        <div className="text-[13px] text-fg">{label}</div>
        {hint && <div className="text-[11.5px] leading-snug text-subtle">{hint}</div>}
      </div>
      {children}
    </div>
  );
}
