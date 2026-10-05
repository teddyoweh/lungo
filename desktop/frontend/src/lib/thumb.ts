// Miniature previews of terminals, drawn from what each terminal's buffer holds right now.
// Text becomes small blocks in its own colour, the way a code minimap does it. This works for
// panes that are not on screen (no GPU surface to copy from) and costs a few milliseconds.
import type { IBufferCell, ITheme, Terminal } from "@xterm/xterm";

const ANSI: (keyof ITheme)[] = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white", "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan", "brightWhite"];
const FALLBACK = ["#1c1c21", "#f2555a", "#3ecf8e", "#f5c451", "#6cb6ff", "#c49bff", "#5ad1e8", "#d4d4d8", "#62626c", "#ff7b80", "#74e8b9", "#fde68a", "#9ccfff", "#dcc2ff", "#9df3ff", "#fafafa"];

function palette(theme: ITheme, i: number): string {
  if (i < 16) return (theme[ANSI[i]] as string | undefined) ?? FALLBACK[i];
  if (i < 232) {
    const n = i - 16;
    const v = (k: number) => (k === 0 ? 0 : 55 + k * 40);
    return `rgb(${v(Math.floor(n / 36))},${v(Math.floor(n / 6) % 6)},${v(n % 6)})`;
  }
  const g = 8 + (i - 232) * 10;
  return `rgb(${g},${g},${g})`;
}

const rgb = (n: number) => `rgb(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255})`;

function fg(cell: IBufferCell, theme: ITheme): string {
  if (cell.isFgRGB()) return rgb(cell.getFgColor());
  if (cell.isFgPalette()) return palette(theme, cell.getFgColor());
  return theme.foreground ?? "#e4e4e7";
}

function bg(cell: IBufferCell, theme: ITheme): string | null {
  if (cell.isBgRGB()) return rgb(cell.getBgColor());
  if (cell.isBgPalette()) return palette(theme, cell.getBgColor());
  return null;
}

// Advance of one character per pixel of font size, per font family (measured once).
const advances = new Map<string, number>();
function advance(ctx: CanvasRenderingContext2D, family: string): number {
  let a = advances.get(family);
  if (!a) {
    ctx.font = `100px ${family}`;
    a = ctx.measureText("M").width / 100 || 0.6;
    advances.set(family, a);
  }
  return a;
}

/**
 * Draws what a terminal shows into the rectangle (x, y, w, h) of a 2D canvas, in canvas pixels.
 * With enough room per character the text itself is drawn, small; below that, blocks.
 */
export function drawTerminal(ctx: CanvasRenderingContext2D, term: Terminal, x: number, y: number, w: number, h: number, minRow = 0) {
  const theme = term.options.theme ?? {};
  const ground = theme.background ?? "#000";
  ctx.fillStyle = ground;
  ctx.fillRect(x, y, w, h);
  const buf = term.buffer.active;
  const family = term.options.fontFamily || "ui-monospace, Menlo, monospace";
  if (!term.cols || !term.rows) return;
  const pad = Math.min(w, h) * 0.03;
  let cols = term.cols;
  let rows = term.rows;
  let cw = (w - pad * 2) / cols;
  let ch = (h - pad * 2) / rows;
  let first = 0; // the first row drawn
  // Too small to read: zoom in on the latest output instead. Rows are made minRow high,
  // the last lines that have anything in them are shown, from the left edge.
  if (minRow > 0 && ch < minRow) {
    let last = rows - 1;
    while (last > 0 && !buf.getLine(buf.viewportY + last)?.translateToString(true).trim()) last--;
    ch = minRow;
    const fit = Math.max(1, Math.floor((h - pad * 2) / ch));
    first = Math.max(0, last - fit + 1);
    rows = Math.min(fit, term.rows - first);
    cw = ch * (advance(ctx, family) / 1.22); // a terminal cell's own proportions
    cols = Math.min(term.cols, Math.max(1, Math.floor((w - pad * 2) / cw)));
  }
  const size = Math.min(cw / advance(ctx, family), ch * 0.86);
  const text = size >= 4.5; // smaller than this, letters are smudges and blocks read better
  if (text) {
    ctx.font = `${size}px ${family}`;
    ctx.textBaseline = "middle";
  }
  ctx.save();
  ctx.beginPath();
  ctx.rect(x, y, w, h);
  ctx.clip();
  const cell = buf.getNullCell();
  for (let r = 0; r < rows; r++) {
    const line = buf.getLine(buf.viewportY + first + r);
    if (!line) continue;
    const top = y + pad + r * ch;
    let runStart = -1;
    let runColor = "";
    let runThin = false;
    let runAlpha = 1;
    let runText = "";
    const flush = (end: number) => {
      if (runStart < 0) return;
      ctx.globalAlpha = runAlpha;
      ctx.fillStyle = runColor;
      const left = x + pad + runStart * cw;
      if (text && !runThin) ctx.fillText(runText, left, top + ch / 2);
      else {
        const bh = runThin ? Math.max(1, ch * 0.12) : ch * 0.5;
        ctx.fillRect(left, top + (ch - bh) / 2, (end - runStart) * cw, bh);
      }
      runStart = -1;
      runText = "";
    };
    for (let c = 0; c < cols; c++) {
      line.getCell(c, cell);
      const chars = cell.getChars();
      const inverse = cell.isInverse() !== 0;
      const back = inverse ? fg(cell, theme) : bg(cell, theme);
      if (back) {
        flush(c);
        ctx.globalAlpha = 0.9;
        ctx.fillStyle = back;
        ctx.fillRect(x + pad + c * cw, top, cw + 0.5, ch + 0.5);
      }
      if (!chars || chars === " ") {
        if (text && runStart >= 0 && !runThin && cell.getWidth() === 1) runText += " "; // keep the run going across a space
        else flush(c);
        continue;
      }
      const code = chars.codePointAt(0) ?? 0;
      const thin = code >= 0x2500 && code <= 0x257f; // box drawing: rules and borders
      const color = inverse ? ground : fg(cell, theme);
      const alpha = cell.isDim() ? 0.5 : text ? 1 : 0.82;
      const wide = cell.getWidth() !== 1; // wide characters would push the rest of a run out of place
      if (runStart >= 0 && (color !== runColor || thin !== runThin || alpha !== runAlpha || wide)) flush(c);
      if (runStart < 0) {
        runStart = c;
        runColor = color;
        runThin = thin;
        runAlpha = alpha;
      }
      runText += chars;
      if (wide) flush(c + 1);
    }
    flush(cols);
  }
  ctx.restore();
  ctx.globalAlpha = 1;
}
