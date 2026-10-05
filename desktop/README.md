# Lungo desktop

The desktop app is two tools in one window: a manager for your machines and a terminal for Claude Code sessions. It runs on the same engine as the `sky` CLI (`internal/engine`) and shares its state in `~/.skybuild`. A machine you create in one shows up in the other.

It's built with Wails v2: a Go backend and a React + TypeScript + Tailwind frontend in the system webview. The terminals use xterm.js connected to real pseudo-terminals.

## Develop

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@latest   # once
cd desktop
wails dev            # native window with hot reload
```

`wails dev` also serves the app at http://localhost:34115. You can open it in a browser, and the Go methods stay callable, so browser devtools work. Shortcuts that the native menu normally handles, like ⌘K and ⌘T, are handled in JavaScript there.

Run the frontend checks on their own:

```sh
cd desktop/frontend
npm install
npx tsc --noEmit
npm run build
```

## Build

```sh
cd desktop
wails build -ldflags "-X main.Version=0.1.0"        # → build/bin/Lungo.app (macOS)
wails build -platform darwin/universal              # Intel + Apple silicon
wails build -platform windows/amd64                 # cross-compiles from macOS (WebView2)
```

Linux builds need a Linux host with `libgtk-3-dev` and `libwebkit2gtk-4.1-dev`. Run `wails build -tags webkit2_41` there.

The app icon is Dawn (`build/appicon.png`); the other icons people can pick in Settings are in `appicons/`, with small copies for the picker in `frontend/src/assets/app-icons/`. The pick is kept in `~/.skybuild/app-icon`. On macOS it shows in the Dock right away, in every window, and also becomes the app's icon in Finder so the Dock keeps it after quitting. On Linux it applies at the next launch.

The app was called Skybuild. It keeps that name where changing it would cost something: the bundle ID `com.wails.skybuild` (macOS keys the web view's storage and notification permission to it), `~/.skybuild`, the `skybuild` tmux socket and the names of cloud resources.

After you change bound Go methods, regenerate the TypeScript bindings with `wails generate module`.

## How it fits together

- **Bindings (`app.go`).** Every exported method on `App` can be called from TypeScript (`frontend/src/wailsjs`). The UI wraps those calls in `frontend/src/lib/api.ts` with plain interfaces that mirror the Go JSON.
- **Long operations (`ops.go`).** Creating, starting, stopping, resizing, deleting, syncing, copying, cloning, logging in and minting a token all return an op id straight away. The work runs in a goroutine and streams `events.Event`s as `op` events, then finishes with one `op:end` (`{op, error, result}`). The UI renders them as step lists with spinners, a collapsible raw log and toasts. Tailscale approval links are also opened in the browser.
- **Terminal bridge (`internal/term`).** Each tab runs a command in a pseudo-terminal: `creack/pty` on macOS and Linux, ConPTY on Windows. A WebSocket server on `127.0.0.1:<random>` serves it, protected by a random token. Binary frames carry terminal bytes; text frames carry `{"type":"resize","cols","rows"}`. Terminals outlive their WebSocket: a reconnecting tab gets the last 512 KB replayed. Machine tabs run `engine.AttachArgs`, which is `ssh -t … tmux new-session -A -s <session>`. Closing a tab only detaches; the session keeps running on the machine.
- **Background poller (`poller.go`).** Every 5 s it lists tmux sessions on every running machine. Claude's state comes from the `sky-session-hook` that sync installs. When a session starts waiting on you, or finishes working, you get a native notification. Machine status refreshes every 60 s.
- **Keyboard.** The native menu provides ⌘K (palette), ⌘T (new session), ⌘L (local terminal), ⌘W (close tab), ⌘N (new machine), ⌘1–9 (tabs) and ⌥⌘1–6 (views). Menu shortcuts work even while a terminal has focus. On Windows and Linux they are Ctrl+Shift+key, so plain Ctrl keys still reach the shell. In a terminal, ⇧↵ (or ⌥↵) sends a newline to Claude Code.
