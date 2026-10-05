// Themes. One object restyles the whole app: the chrome (the CSS variables in index.css) and
// the terminal palette, so a pane never sits in a frame of another colour.
import type { ITheme } from "@xterm/xterm";

export interface AppTheme {
  id: string;
  name: string;
  dark: boolean;
  chrome: Chrome;
  term: ITheme;
}

/** The colours a theme chooses; the rest of the tokens are derived from them. */
interface Chrome {
  bg: string; // pages and the pane area
  panel: string; // cards
  raised: string; // menus, dialogs
  sidebar: string; // solid; shown through the window's vibrancy on macOS
  ink: string; // "r, g, b" that hover, selection and hairlines are tinted with
  alphas?: [hover: number, active: number, line: number, lineStrong: number]; // how strong those are
  sidebarAlpha?: number; // how solid the sidebar is over the window's vibrancy, in percent
  fg: string;
  muted: string;
  subtle: string;
  accent: string;
  accentStrong: string;
  accentFg: string; // text on an accent-coloured button
  green: string;
  amber: string;
  red: string;
  violet: string;
  blue: string;
}

const ansi = (
  normal: [string, string, string, string, string, string, string, string],
  bright: [string, string, string, string, string, string, string, string],
): Partial<ITheme> => ({
  black: normal[0],
  red: normal[1],
  green: normal[2],
  yellow: normal[3],
  blue: normal[4],
  magenta: normal[5],
  cyan: normal[6],
  white: normal[7],
  brightBlack: bright[0],
  brightRed: bright[1],
  brightGreen: bright[2],
  brightYellow: bright[3],
  brightBlue: bright[4],
  brightMagenta: bright[5],
  brightCyan: bright[6],
  brightWhite: bright[7],
});

/** Screen, cursor, selection and scrollbar colours from a background, a text colour and an accent. */
function screen(bg: string, fg: string, cursor: string, sel: string, dark: boolean): Partial<ITheme> {
  const bar = dark ? "255,255,255" : "0,0,0";
  return {
    background: bg,
    foreground: fg,
    cursor,
    cursorAccent: bg,
    selectionBackground: `rgba(${sel},${dark ? 0.3 : 0.2})`,
    selectionInactiveBackground: `rgba(${sel},${dark ? 0.16 : 0.1})`,
    scrollbarSliderBackground: `rgba(${bar},0.10)`,
    scrollbarSliderHoverBackground: `rgba(${bar},0.18)`,
    scrollbarSliderActiveBackground: `rgba(${bar},0.24)`,
  };
}

export const THEMES: AppTheme[] = [
  {
    // Pure black, thin separators, crisp text. The default.
    id: "black",
    name: "Black",
    dark: true,
    chrome: {
      bg: "#000000",
      panel: "#0a0a0a",
      raised: "#111111",
      sidebar: "#050505",
      ink: "255, 255, 255",
      alphas: [0.075, 0.115, 0.12, 0.2], // on pure black a hairline needs more to show (#1f1f1f)
      sidebarAlpha: 97,
      fg: "#fafafa",
      muted: "#a3a3a3",
      subtle: "#757575",
      accent: "#6cb6ff",
      accentStrong: "#8cc6ff",
      accentFg: "#06121f",
      green: "#3ecf8e",
      amber: "#f5a524",
      red: "#f2555a",
      violet: "#9d8cff",
      blue: "#6cb6ff",
    },
    term: {
      ...screen("#000000", "#f4f4f5", "#8cc6ff", "108,182,255", true),
      ...ansi(
        ["#1c1c1c", "#f2555a", "#3ecf8e", "#f5c451", "#6cb6ff", "#c49bff", "#5ad1e8", "#d4d4d8"],
        ["#6b6b6b", "#ff7b80", "#74e8b9", "#fde68a", "#9ccfff", "#dcc2ff", "#9df3ff", "#ffffff"],
      ),
    },
  },
  {
    id: "midnight",
    name: "Midnight",
    dark: true,
    chrome: {
      bg: "#0e0e10",
      panel: "#121214",
      raised: "#17171a",
      sidebar: "#111113",
      ink: "255, 255, 255",
      fg: "#ececef",
      muted: "#a1a1aa",
      subtle: "#6e6e78",
      accent: "#6cb6ff",
      accentStrong: "#8cc6ff",
      accentFg: "#06121f",
      green: "#3ecf8e",
      amber: "#f5a524",
      red: "#f2555a",
      violet: "#9d8cff",
      blue: "#6cb6ff",
    },
    term: {
      ...screen("#0e0e10", "#e4e4e7", "#8cc6ff", "108,182,255", true),
      ...ansi(
        ["#1c1c21", "#f2555a", "#3ecf8e", "#f5c451", "#6cb6ff", "#c49bff", "#5ad1e8", "#d4d4d8"],
        ["#62626c", "#ff7b80", "#74e8b9", "#fde68a", "#9ccfff", "#dcc2ff", "#9df3ff", "#fafafa"],
      ),
    },
  },
  {
    id: "tokyo-night",
    name: "Tokyo Night",
    dark: true,
    chrome: {
      bg: "#1a1b26",
      panel: "#1d1e2b",
      raised: "#222436",
      sidebar: "#16161e",
      ink: "192, 202, 245",
      fg: "#c0caf5",
      muted: "#9aa5ce",
      subtle: "#565f89",
      accent: "#7aa2f7",
      accentStrong: "#9ab8ff",
      accentFg: "#10121c",
      green: "#9ece6a",
      amber: "#e0af68",
      red: "#f7768e",
      violet: "#bb9af7",
      blue: "#7aa2f7",
    },
    term: {
      ...screen("#1a1b26", "#c0caf5", "#c0caf5", "122,162,247", true),
      ...ansi(
        ["#15161e", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#a9b1d6"],
        ["#414868", "#ff899d", "#9fe044", "#faba4a", "#8db0ff", "#c7a9ff", "#a4daff", "#c0caf5"],
      ),
    },
  },
  {
    id: "catppuccin-mocha",
    name: "Catppuccin Mocha",
    dark: true,
    chrome: {
      bg: "#1e1e2e",
      panel: "#212234",
      raised: "#27283b",
      sidebar: "#181825",
      ink: "205, 214, 244",
      fg: "#cdd6f4",
      muted: "#a6adc8",
      subtle: "#6c7086",
      accent: "#89b4fa",
      accentStrong: "#b4befe",
      accentFg: "#11111b",
      green: "#a6e3a1",
      amber: "#f9e2af",
      red: "#f38ba8",
      violet: "#cba6f7",
      blue: "#89b4fa",
    },
    term: {
      ...screen("#1e1e2e", "#cdd6f4", "#f5e0dc", "137,180,250", true),
      ...ansi(
        ["#45475a", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#bac2de"],
        ["#585b70", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#a6adc8"],
      ),
    },
  },
  {
    id: "gruvbox-dark",
    name: "Gruvbox Dark",
    dark: true,
    chrome: {
      bg: "#282828",
      panel: "#2c2b2a",
      raised: "#32302f",
      sidebar: "#1d2021",
      ink: "235, 219, 178",
      fg: "#ebdbb2",
      muted: "#bdae93",
      subtle: "#928374",
      accent: "#fabd2f",
      accentStrong: "#fdd26a",
      accentFg: "#1d2021",
      green: "#b8bb26",
      amber: "#fe8019",
      red: "#fb4934",
      violet: "#d3869b",
      blue: "#83a598",
    },
    term: {
      ...screen("#282828", "#ebdbb2", "#ebdbb2", "250,189,47", true),
      ...ansi(
        ["#282828", "#cc241d", "#98971a", "#d79921", "#458588", "#b16286", "#689d6a", "#a89984"],
        ["#928374", "#fb4934", "#b8bb26", "#fabd2f", "#83a598", "#d3869b", "#8ec07c", "#ebdbb2"],
      ),
    },
  },
  {
    id: "nord",
    name: "Nord",
    dark: true,
    chrome: {
      bg: "#2e3440",
      panel: "#323946",
      raised: "#3b4252",
      sidebar: "#272c36",
      ink: "216, 222, 233",
      fg: "#e5e9f0",
      muted: "#b8c0cf",
      subtle: "#7b88a1",
      accent: "#88c0d0",
      accentStrong: "#8fbcbb",
      accentFg: "#20242c",
      green: "#a3be8c",
      amber: "#ebcb8b",
      red: "#bf616a",
      violet: "#b48ead",
      blue: "#81a1c1",
    },
    term: {
      ...screen("#2e3440", "#d8dee9", "#d8dee9", "136,192,208", true),
      ...ansi(
        ["#3b4252", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#88c0d0", "#e5e9f0"],
        ["#4c566a", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#8fbcbb", "#eceff4"],
      ),
    },
  },
  {
    id: "dracula",
    name: "Dracula",
    dark: true,
    chrome: {
      bg: "#282a36",
      panel: "#2c2e3b",
      raised: "#343746",
      sidebar: "#21222c",
      ink: "248, 248, 242",
      fg: "#f8f8f2",
      muted: "#c3c5d4",
      subtle: "#7f86b0",
      accent: "#bd93f9",
      accentStrong: "#d0b0ff",
      accentFg: "#1e1f29",
      green: "#50fa7b",
      amber: "#ffb86c",
      red: "#ff5555",
      violet: "#bd93f9",
      blue: "#8be9fd",
    },
    term: {
      ...screen("#282a36", "#f8f8f2", "#f8f8f2", "68,71,90", true),
      ...ansi(
        ["#21222c", "#ff5555", "#50fa7b", "#f1fa8c", "#bd93f9", "#ff79c6", "#8be9fd", "#f8f8f2"],
        ["#6272a4", "#ff6e6e", "#69ff94", "#ffffa5", "#d6acff", "#ff92df", "#a4ffff", "#ffffff"],
      ),
    },
  },
  {
    id: "one-dark",
    name: "One Dark",
    dark: true,
    chrome: {
      bg: "#282c34",
      panel: "#2c313a",
      raised: "#333842",
      sidebar: "#21252b",
      ink: "171, 178, 191",
      fg: "#d7dae0",
      muted: "#abb2bf",
      subtle: "#7a8191",
      accent: "#61afef",
      accentStrong: "#7fc0f5",
      accentFg: "#0f1621",
      green: "#98c379",
      amber: "#e5c07b",
      red: "#e06c75",
      violet: "#c678dd",
      blue: "#61afef",
    },
    term: {
      ...screen("#282c34", "#abb2bf", "#528bff", "97,175,239", true),
      ...ansi(
        ["#3f4451", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#abb2bf"],
        ["#5c6370", "#ef7b84", "#a9d38b", "#f0ce8f", "#74bdf7", "#d48ae9", "#68c6d2", "#e6e6e6"],
      ),
    },
  },
  {
    id: "rose-pine",
    name: "Rosé Pine",
    dark: true,
    chrome: {
      bg: "#191724",
      panel: "#1f1d2e",
      raised: "#26233a",
      sidebar: "#16141f",
      ink: "224, 222, 244",
      fg: "#e0def4",
      muted: "#908caa",
      subtle: "#6e6a86",
      accent: "#c4a7e7",
      accentStrong: "#d6c0f0",
      accentFg: "#191724",
      green: "#9ccfd8",
      amber: "#f6c177",
      red: "#eb6f92",
      violet: "#c4a7e7",
      blue: "#9ccfd8",
    },
    term: {
      ...screen("#191724", "#e0def4", "#e0def4", "110,106,134", true),
      ...ansi(
        ["#26233a", "#eb6f92", "#31748f", "#f6c177", "#9ccfd8", "#c4a7e7", "#ebbcba", "#e0def4"],
        ["#6e6a86", "#eb6f92", "#31748f", "#f6c177", "#9ccfd8", "#c4a7e7", "#ebbcba", "#e0def4"],
      ),
    },
  },
  {
    id: "light",
    name: "Light",
    dark: false,
    chrome: {
      bg: "#ffffff",
      panel: "#fbfbfc",
      raised: "#ffffff",
      sidebar: "#f6f6f8",
      ink: "0, 0, 0",
      fg: "#18181b",
      muted: "#55555f",
      subtle: "#8b8b95",
      accent: "#1a73e8",
      accentStrong: "#135cc0",
      accentFg: "#ffffff",
      green: "#12a150",
      amber: "#c27803",
      red: "#dc2f36",
      violet: "#6d55e8",
      blue: "#1a73e8",
    },
    term: {
      ...screen("#ffffff", "#1f1f23", "#1a73e8", "26,115,232", false),
      ...ansi(
        ["#1f1f23", "#cf222e", "#1a7f37", "#9a6700", "#0969da", "#8250df", "#1b7c83", "#6e7781"],
        ["#57606a", "#a40e26", "#2da44e", "#bf8700", "#218bff", "#a475f9", "#3192aa", "#8c959f"],
      ),
    },
  },
  {
    id: "github-light",
    name: "GitHub Light",
    dark: false,
    chrome: {
      bg: "#ffffff",
      panel: "#f6f8fa",
      raised: "#ffffff",
      sidebar: "#f6f8fa",
      ink: "31, 35, 40",
      fg: "#1f2328",
      muted: "#59636e",
      subtle: "#818b98",
      accent: "#0969da",
      accentStrong: "#0550ae",
      accentFg: "#ffffff",
      green: "#1a7f37",
      amber: "#9a6700",
      red: "#cf222e",
      violet: "#8250df",
      blue: "#0969da",
    },
    term: {
      ...screen("#ffffff", "#1f2328", "#0969da", "9,105,218", false),
      ...ansi(
        ["#24292f", "#cf222e", "#116329", "#4d2d00", "#0969da", "#8250df", "#1b7c83", "#6e7781"],
        ["#57606a", "#a40e26", "#1a7f37", "#633c01", "#218bff", "#a475f9", "#3192aa", "#8c959f"],
      ),
    },
  },
  {
    id: "solarized-light",
    name: "Solarized Light",
    dark: false,
    chrome: {
      bg: "#fdf6e3",
      panel: "#f7f0dc",
      raised: "#fdf6e3",
      sidebar: "#eee8d5",
      ink: "88, 110, 117",
      fg: "#073642",
      muted: "#586e75",
      subtle: "#93a1a1",
      accent: "#268bd2",
      accentStrong: "#1a6ea8",
      accentFg: "#ffffff",
      green: "#859900",
      amber: "#b58900",
      red: "#dc322f",
      violet: "#6c71c4",
      blue: "#268bd2",
    },
    term: {
      ...screen("#fdf6e3", "#586e75", "#268bd2", "38,139,210", false),
      ...ansi(
        ["#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5"],
        ["#002b36", "#cb4b16", "#586e75", "#657b83", "#839496", "#6c71c4", "#93a1a1", "#fdf6e3"],
      ),
    },
  },
];

export const DEFAULT_DARK = "black";
export const DEFAULT_LIGHT = "light";

export function themeById(id: string | null | undefined, dark: boolean): AppTheme {
  return THEMES.find((t) => t.id === id && t.dark === dark) ?? THEMES.find((t) => t.id === (dark ? DEFAULT_DARK : DEFAULT_LIGHT))!;
}

/** The CSS variables for a theme's chrome (the names index.css uses). */
function tokens(t: AppTheme): Record<string, string> {
  const c = t.chrome;
  const ink = (a: number) => `rgba(${c.ink}, ${a})`;
  const [hover, active, line, lineStrong] = c.alphas ?? (t.dark ? [0.045, 0.075, 0.075, 0.13] : [0.04, 0.065, 0.08, 0.14]);
  return {
    "--bg": c.bg,
    "--panel": c.panel,
    "--raised": c.raised,
    "--hover": ink(hover),
    "--active": ink(active),
    "--sidebar": `color-mix(in srgb, ${c.sidebar} ${c.sidebarAlpha ?? 84}%, transparent)`,
    "--sidebar-solid": c.sidebar,
    "--line": ink(line),
    "--line-strong": ink(lineStrong),
    "--fg": c.fg,
    "--fg-muted": c.muted,
    "--fg-subtle": c.subtle,
    "--accent": c.accent,
    "--accent-strong": c.accentStrong,
    "--accent-soft": `color-mix(in srgb, ${c.accent} 14%, transparent)`,
    "--accent-fg": c.accentFg,
    "--green": c.green,
    "--amber": c.amber,
    "--red": c.red,
    "--violet": c.violet,
    "--blue": c.blue,
    "--shadow": t.dark ? `0 0 0 1px ${ink(0.06)}, 0 16px 48px rgba(0, 0, 0, 0.55)` : `0 0 0 1px ${ink(0.08)}, 0 16px 48px rgba(0, 0, 0, 0.16)`,
    "--term-bg": t.term.background ?? c.bg,
  };
}

/**
 * One stylesheet with a rule per theme, keyed by data-skin on the root element. Applying a
 * theme is then two attributes: data-theme (dark or light, which index.css and the system
 * colour scheme key on) and data-skin.
 */
export function installThemes() {
  if (document.getElementById("sky-themes")) return;
  const css = THEMES.map((t) => {
    const sel = `:root[data-theme][data-skin="${t.id}"]`;
    const body = Object.entries(tokens(t))
      .map(([k, v]) => `${k}: ${v};`)
      .join(" ");
    // Without window vibrancy (not macOS, or a browser) the sidebar is painted solid.
    return `${sel} { ${body} }\n${sel}:not([data-platform="darwin"]) { --sidebar: var(--sidebar-solid); }`;
  }).join("\n");
  const el = document.createElement("style");
  el.id = "sky-themes";
  el.textContent = css;
  document.head.appendChild(el);
}
