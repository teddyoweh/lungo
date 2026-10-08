// Whether this window can be seen, as the app says. On macOS the web view is told not to
// judge that itself: WebKit lets the web process of a covered window sleep, which throws
// away the window's picture, so Mission Control and the swipe between Spaces showed it
// blank. The page reports the app's answer as its own visibility instead, so everything
// that rests while the page is hidden (polls, flushing settings) still does, and
// animations pause. Until the app first says, the page's own visibility stands.
import { EventsOn } from "../wailsjs/runtime/runtime";

let shown: boolean | undefined;

if ((window as unknown as { runtime?: unknown }).runtime) {
  EventsOn("window-shown", (v: boolean) => {
    if (shown === undefined) {
      Object.defineProperty(document, "hidden", { configurable: true, get: () => !shown });
      Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (shown ? "visible" : "hidden") });
    }
    if (v === shown) return;
    shown = v;
    document.documentElement.toggleAttribute("data-covered", !shown);
    document.dispatchEvent(new Event("visibilitychange"));
  });
}
