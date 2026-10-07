// The page starts in two steps: the settings the app keeps go into the page storage first
// (see lib/prefs), then the app itself is loaded, whose modules read that storage as they
// start (the theme, session names, workspaces, …).
import { loadPrefs } from "./lib/prefs";

await loadPrefs();
await import("./start");
