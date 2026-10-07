// The page's settings (sky.* in its storage: session names and colours, workspaces, theme,
// terminal look, each window's zoom and sidebar, …) are kept by the app too, in
// ~/.skybuild/state/prefs.json (desktop/prefs.go). Windows are separate processes and only
// one of them gets its page storage written to disk, so what any other window saved used to
// be gone after a restart.
//
// Before the page starts, the app's copy goes into the page storage (loadPrefs, from
// main.tsx), so everything that reads it while starting sees the real values. After that,
// every write of a setting also goes to the app (the storage's own setItem and removeItem
// are wrapped, so no write is missed wherever it comes from), and what other windows change
// arrives as "prefs" events (takePrefs). This file imports nothing of the app's: it runs
// before the rest is loaded.

/** Whether a page storage key is a setting kept by the app. Pane snapshots (large, one per
 * pane), the tab layout (kept in its own file) and the last session list (a cache) aren't. */
export function kept(key: string): boolean {
  return key.startsWith("sky.") && !key.startsWith("sky.snap.") && !key.startsWith("sky.layout.v1") && key !== "sky.sessions.last";
}

type Binding = {
  Prefs?: () => Promise<Record<string, string>>;
  SetPrefs?: (changes: Record<string, string>) => Promise<void>;
};
const app = (): Binding | undefined => (window as unknown as { go?: { main?: { App?: Binding } } }).go?.main?.App;

// Settings changed here and not yet sent (an empty value removes), and the ones sent whose
// answer hasn't come back: news from other windows doesn't overwrite either.
const pending = new Map<string, string>();
const sending = new Map<string, number>();
let timer: number | undefined;
let applying = false; // writing what the app sent: not to be sent back

function queue(key: string, value: string) {
  pending.set(key, value);
  window.clearTimeout(timer);
  timer = window.setTimeout(flushPrefs, 200);
}

/** Sends what changed now (also when the page is about to go). */
export function flushPrefs() {
  window.clearTimeout(timer);
  timer = undefined;
  if (!pending.size) return;
  const set = app()?.SetPrefs;
  const batch = Object.fromEntries(pending);
  pending.clear();
  if (!set) return;
  for (const k of Object.keys(batch)) sending.set(k, (sending.get(k) ?? 0) + 1);
  const done = () => {
    for (const k of Object.keys(batch)) {
      const n = (sending.get(k) ?? 1) - 1;
      if (n > 0) sending.set(k, n);
      else sending.delete(k);
    }
  };
  set(batch).then(done, done);
}

let hooked = false;
function hook() {
  if (hooked) return;
  hooked = true;
  const set = Storage.prototype.setItem;
  const remove = Storage.prototype.removeItem;
  Storage.prototype.setItem = function (this: Storage, key: string, value: string) {
    set.call(this, key, value);
    if (this === window.localStorage && !applying && kept(key)) queue(key, String(value));
  };
  Storage.prototype.removeItem = function (this: Storage, key: string) {
    remove.call(this, key);
    if (this === window.localStorage && !applying && kept(key)) queue(key, "");
  };
  window.addEventListener("pagehide", flushPrefs);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") flushPrefs();
  });
}

function storedKeys(): string[] {
  const out: string[] = [];
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k && kept(k)) out.push(k);
    }
  } catch {
    /* storage unavailable */
  }
  return out;
}

/**
 * Puts the app's settings into the page storage (the full set: a setting missing from it was
 * removed in some window). Keys changed here and not yet saved are left alone. It returns the
 * keys whose value changed.
 */
export function takePrefs(prefs: Record<string, string>): Set<string> {
  const changed = new Set<string>();
  applying = true;
  try {
    for (const [k, v] of Object.entries(prefs)) {
      if (!kept(k) || pending.has(k) || sending.has(k)) continue;
      if (localStorage.getItem(k) !== v) {
        localStorage.setItem(k, v);
        changed.add(k);
      }
    }
    for (const k of storedKeys()) {
      if (!(k in prefs) && !pending.has(k) && !sending.has(k)) {
        localStorage.removeItem(k);
        changed.add(k);
      }
    }
  } catch {
    /* storage unavailable */
  } finally {
    applying = false;
  }
  return changed;
}

/**
 * Before the page starts: the app's settings into the page storage, and from then on every
 * change goes to the app as well. The very first time (nothing kept yet), this window's
 * settings become the app's. Without the app (a plain browser) the page storage is all.
 */
export async function loadPrefs() {
  hook();
  const get = app()?.Prefs;
  if (!get) return;
  let prefs: Record<string, string> | undefined;
  try {
    prefs = await Promise.race([get(), new Promise<undefined>((done) => window.setTimeout(() => done(undefined), 2500))]);
  } catch {
    return;
  }
  if (!prefs) return; // the app didn't answer in time: start with what the page has
  if (!Object.keys(prefs).length) {
    for (const k of storedKeys()) {
      const v = localStorage.getItem(k);
      if (v !== null && v !== "") pending.set(k, v);
    }
    flushPrefs();
    return;
  }
  takePrefs(prefs);
}
