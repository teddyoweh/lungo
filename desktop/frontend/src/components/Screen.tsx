// A machine's screen, live: you see what is on it and can use it, from here. A Mac shares its
// screen with macOS's own Screen Sharing; the app reaches it through the ssh connection it
// already has (nothing is opened to the network) and signs in with a user of that Mac, whose
// name and password are kept in this computer's keychain.
import { useEffect, useRef, useState } from "react";
import RFB from "@novnc/novnc";
import { ClipboardPaste, Eye, Maximize, Minimize, MousePointer2, RotateCcw, X } from "lucide-react";
import { api, errText, type ScreenView } from "../lib/api";
import { closeScreen, useScreen } from "../lib/screen";
import { useStore } from "../lib/store";
import { cx } from "../lib/util";
import { ProviderIcon } from "./Brand";
import { IconButton, Spinner } from "./ui";

type Phase = "opening" | "login" | "connecting" | "live" | "ended";

export function ScreenOverlay() {
  const machine = useScreen();
  if (!machine) return null;
  return <Screen key={machine} machine={machine} />;
}

function Screen({ machine }: { machine: string }) {
  const m = useStore((s) => s.machines.find((x) => x.machine.name === machine)?.machine);
  const host = useRef<HTMLDivElement>(null);
  const rfb = useRef<RFB | null>(null);
  const view = useRef<ScreenView | null>(null);
  const typed = useRef<{ user: string; password: string } | null>(null); // a login given just now, kept once it works
  const last = useRef<{ user: string; password: string } | null>(null); // what the last try signed in with
  const [phase, setPhase] = useState<Phase>("opening");
  const [note, setNote] = useState(""); // why it isn't showing
  const [name, setName] = useState(""); // what the Mac calls its screen
  const [watch, setWatch] = useState(false); // look, don't touch
  const [fit, setFit] = useState(true);
  const [user, setUser] = useState("");
  const [password, setPassword] = useState("");

  const drop = () => {
    const r = rfb.current;
    rfb.current = null;
    try {
      r?.disconnect();
    } catch {
      /* already gone */
    }
  };

  const connect = (u: string, p: string) => {
    const v = view.current;
    if (!v || !host.current) return;
    drop();
    last.current = { user: u, password: p };
    setPhase("connecting");
    setNote("");
    let live = false;
    let refused = false;
    const r = new RFB(host.current, v.url, { shared: true, credentials: { username: u, password: p } });
    rfb.current = r;
    r.scaleViewport = fit;
    r.clipViewport = !fit;
    r.dragViewport = false;
    r.resizeSession = false;
    r.viewOnly = watch;
    r.focusOnClick = true;
    r.background = "transparent";
    r.qualityLevel = 7;
    r.compressionLevel = 2;
    r.addEventListener("connect", () => {
      if (rfb.current !== r) return;
      live = true;
      setPhase("live");
      r.focus();
      // A login typed just now worked: it is kept for next time.
      if (typed.current) void api.saveScreenLogin(machine, typed.current.user, typed.current.password).catch(() => {});
      typed.current = null;
    });
    r.addEventListener("desktopname", (e) => setName((e as CustomEvent<{ name: string }>).detail.name));
    r.addEventListener("credentialsrequired", () => {
      if (rfb.current !== r) return;
      refused = true;
      setPhase("login");
    });
    r.addEventListener("securityfailure", () => {
      if (rfb.current !== r) return;
      refused = true;
      typed.current = null;
      setNote("That name and password weren't accepted.");
      setPhase("login");
    });
    r.addEventListener("clipboard", (e) => {
      const text = (e as CustomEvent<{ text: string }>).detail.text;
      if (text) void api.copy(text);
    });
    r.addEventListener("disconnect", () => {
      if (rfb.current !== r) return;
      rfb.current = null;
      if (refused) return;
      setNote(live ? "The connection to its screen ended." : `Couldn't reach the screen of ${machine}. On that Mac, Screen Sharing has to be on (System Settings › General › Sharing).`);
      setPhase("ended");
    });
  };

  useEffect(() => {
    let off = false;
    // The terminal behind lets go of the keyboard: what is typed now is for the screen.
    const had = document.activeElement as HTMLElement | null;
    had?.blur();
    api.openScreen(machine).then(
      (v) => {
        if (off) return;
        view.current = v;
        setUser(v.user);
        if (v.password) connect(v.user, v.password);
        else setPhase("login");
      },
      (e) => {
        if (off) return;
        setNote(errText(e));
        setPhase("ended");
      },
    );
    return () => {
      off = true;
      drop();
      had?.focus?.();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [machine]);

  useEffect(() => {
    const r = rfb.current;
    if (!r) return;
    r.viewOnly = watch;
    r.scaleViewport = fit;
    r.clipViewport = !fit;
    r.dragViewport = !fit && watch;
  }, [watch, fit, phase]);

  // Esc puts the screen away, except while you are using it: then Esc is for that Mac.
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || (phase === "live" && !watch && host.current?.contains(document.activeElement))) return;
      e.preventDefault();
      e.stopPropagation();
      closeScreen();
    };
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, [phase, watch]);

  const signIn = (e: React.FormEvent) => {
    e.preventDefault();
    if (!user.trim() || !password) return;
    typed.current = { user: user.trim(), password };
    connect(user.trim(), password);
    setPassword("");
  };
  const paste = async () => {
    const text = await api.clipboardText().catch(() => "");
    if (text) rfb.current?.clipboardPasteFrom(text);
    rfb.current?.focus();
  };

  return (
    <div className="z anim-fade fixed inset-0 z-[65] flex flex-col bg-black/80 px-[2.5vw] pt-[2vh] pb-[3vh] backdrop-blur-sm" onMouseDown={(e) => e.target === e.currentTarget && closeScreen()}>
      <div className="anim-in mx-auto flex min-h-0 w-full flex-1 flex-col overflow-hidden rounded-2xl bg-raised shadow-pop">
        <div className="flex h-[42px] shrink-0 items-center gap-2.5 pr-2 pl-4">
          {m && <ProviderIcon provider={m.provider} os={m.os} size={14} />}
          <span className="text-[13px] font-medium text-fg">{machine}</span>
          <span className="min-w-0 flex-1 truncate text-[11.5px] text-subtle">
            {phase === "live" ? (
              <span className="flex items-center gap-1.5">
                <span className="size-[6px] shrink-0 rounded-full bg-ok" />
                <span className="truncate">
                  {name || "its screen"}
                  {watch ? " · watching only" : " · click it to use it"}
                </span>
              </span>
            ) : phase === "connecting" || phase === "opening" ? (
              "Connecting to its screen…"
            ) : (
              ""
            )}
          </span>
          {phase === "live" && (
            <>
              <IconButton label={watch ? "Watching only. Click to use the mouse and keyboard there" : "Using it. Click to only watch (nothing you do reaches that Mac)"} className="size-7" active={watch} onClick={() => setWatch((w) => !w)}>
                {watch ? <Eye size={14} /> : <MousePointer2 size={14} />}
              </IconButton>
              <IconButton label={fit ? "Fitted to the window. Click for its real size" : "Real size. Click to fit it to the window"} className="size-7" onClick={() => setFit((f) => !f)}>
                {fit ? <Maximize size={14} /> : <Minimize size={14} />}
              </IconButton>
              <IconButton label="Paste this computer's clipboard there" className="size-7" disabled={watch} onClick={() => void paste()}>
                <ClipboardPaste size={14} />
              </IconButton>
            </>
          )}
          <IconButton label="Close" className="size-7" onClick={closeScreen}>
            <X size={15} />
          </IconButton>
        </div>
        <div className="relative min-h-0 flex-1 bg-black">
          <div ref={host} className={cx("absolute inset-0", phase !== "live" && "invisible")} />
          {phase !== "live" && (
            <div className="absolute inset-0 flex items-center justify-center">
              {phase === "login" ? (
                <form onSubmit={signIn} className="flex w-[300px] flex-col gap-2.5">
                  <div className="text-[13px] font-medium text-fg">Sign in to {machine}</div>
                  <div className="text-[11.5px] leading-[1.45] text-subtle">{note || "The name and password of a user on that Mac. They are kept in this computer's keychain."}</div>
                  <input value={user} onChange={(e) => setUser(e.target.value)} autoFocus={!user} spellCheck={false} placeholder="User name" className="h-8 rounded-lg bg-[color-mix(in_srgb,var(--fg)_7%,transparent)] px-2.5 text-[12.5px] text-fg outline-none placeholder:text-subtle" />
                  <input value={password} onChange={(e) => setPassword(e.target.value)} autoFocus={!!user} type="password" placeholder="Password" className="h-8 rounded-lg bg-[color-mix(in_srgb,var(--fg)_7%,transparent)] px-2.5 text-[12.5px] text-fg outline-none placeholder:text-subtle" />
                  <button type="submit" disabled={!user.trim() || !password} className="mt-1 h-8 rounded-lg bg-accent text-[12.5px] font-medium text-accent-fg hover:brightness-110 disabled:opacity-40">
                    Show its screen
                  </button>
                </form>
              ) : phase === "ended" ? (
                <div className="flex w-[340px] flex-col items-center gap-3 text-center text-[12.5px] text-muted">
                  <span>{note}</span>
                  <button onClick={() => (last.current?.password ? connect(last.current.user, last.current.password) : view.current ? setPhase("login") : closeScreen())} className="flex h-8 items-center gap-2 rounded-lg bg-[color-mix(in_srgb,var(--fg)_8%,transparent)] px-3 text-fg hover:bg-hover">
                    <RotateCcw size={13} /> Try again
                  </button>
                </div>
              ) : (
                <Spinner size={16} className="text-subtle" />
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
