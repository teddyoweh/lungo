// Split-pane layout math. A group (one entry in the tab bar) holds a tree of panes: leaves are
// pane keys, inner nodes split their area in a row (side by side) or a column (stacked).
// Everything here is pure; the store applies the results.

export type Layout = { pane: string } | { dir: "row" | "col"; children: Layout[]; sizes: number[] };
export type Dir = "row" | "col";
export type Rect = { x: number; y: number; w: number; h: number }; // fractions of the pane area
export interface Divider {
  path: number[]; // child indexes from the root to the split node
  index: number; // boundary between children[index] and children[index + 1]
  dir: Dir;
  box: Rect; // the split node's area
  at: number; // boundary position as a fraction of the pane area (x for rows, y for columns)
  rect: Rect; // where to draw the handle
}

export const isLeaf = (l: Layout): l is { pane: string } => "pane" in l;

export function leaves(l: Layout): string[] {
  return isLeaf(l) ? [l.pane] : l.children.flatMap(leaves);
}

const norm = (sizes: number[]) => {
  const sum = sizes.reduce((a, b) => a + b, 0) || 1;
  return sizes.map((s) => s / sum);
};

/** Puts newKey next to target: after it (right/below) or before it. */
export function split(l: Layout, target: string, newKey: string, dir: Dir, after = true): Layout {
  if (isLeaf(l)) {
    if (l.pane !== target) return l;
    const kids = after ? [l, { pane: newKey }] : [{ pane: newKey }, l];
    return { dir, children: kids, sizes: [0.5, 0.5] };
  }
  const i = l.children.findIndex((c) => isLeaf(c) && c.pane === target);
  if (i >= 0 && l.dir === dir) {
    // Same direction: add a sibling and share the space evenly, instead of nesting.
    const children = [...l.children];
    children.splice(after ? i + 1 : i, 0, { pane: newKey });
    return { ...l, children, sizes: children.map(() => 1 / children.length) };
  }
  return { ...l, children: l.children.map((c) => split(c, target, newKey, dir, after)) };
}

/** Removes a pane, collapsing splits left with one child. Null when nothing is left. */
export function remove(l: Layout, key: string): Layout | null {
  if (isLeaf(l)) return l.pane === key ? null : l;
  const children: Layout[] = [];
  const sizes: number[] = [];
  l.children.forEach((c, i) => {
    const r = remove(c, key);
    if (r) {
      children.push(r);
      sizes.push(l.sizes[i]);
    }
  });
  if (children.length === 0) return null;
  if (children.length === 1) return children[0];
  return { ...l, children, sizes: norm(sizes) };
}

/** Replaces the sizes of the split node at path. */
export function resize(l: Layout, path: number[], sizes: number[]): Layout {
  if (isLeaf(l)) return l;
  if (path.length === 0) return { ...l, sizes: norm(sizes) };
  const [i, ...rest] = path;
  return { ...l, children: l.children.map((c, j) => (j === i ? resize(c, rest, sizes) : c)) };
}

export function node(l: Layout, path: number[]): Layout {
  return path.reduce<Layout>((n, i) => (isLeaf(n) ? n : n.children[i]), l);
}

/** Pane rectangles and divider handles for drawing. */
export function geometry(l: Layout): { rects: Record<string, Rect>; dividers: Divider[] } {
  const rects: Record<string, Rect> = {};
  const dividers: Divider[] = [];
  const walk = (n: Layout, r: Rect, path: number[]) => {
    if (isLeaf(n)) {
      rects[n.pane] = r;
      return;
    }
    let off = 0;
    n.children.forEach((c, i) => {
      const s = n.sizes[i];
      const cr = n.dir === "row" ? { x: r.x + off * r.w, y: r.y, w: s * r.w, h: r.h } : { x: r.x, y: r.y + off * r.h, w: r.w, h: s * r.h };
      walk(c, cr, [...path, i]);
      off += s;
      if (i < n.children.length - 1) {
        const at = n.dir === "row" ? r.x + off * r.w : r.y + off * r.h;
        dividers.push({
          path,
          index: i,
          dir: n.dir,
          box: r,
          at,
          rect: n.dir === "row" ? { x: at, y: r.y, w: 0, h: r.h } : { x: r.x, y: at, w: r.w, h: 0 },
        });
      }
    });
  };
  walk(l, { x: 0, y: 0, w: 1, h: 1 }, []);
  return { rects, dividers };
}

/** The pane next to key in a direction, picking the one that overlaps most along the edge. */
export function neighbor(l: Layout, key: string, dir: "left" | "right" | "up" | "down"): string | undefined {
  const { rects } = geometry(l);
  const a = rects[key];
  if (!a) return undefined;
  const eps = 1e-6;
  let best: string | undefined;
  let bestScore = 0;
  for (const [k, b] of Object.entries(rects)) {
    if (k === key) continue;
    let touches = false;
    let overlap = 0;
    if (dir === "left" || dir === "right") {
      touches = dir === "left" ? Math.abs(b.x + b.w - a.x) < eps : Math.abs(a.x + a.w - b.x) < eps;
      overlap = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y);
    } else {
      touches = dir === "up" ? Math.abs(b.y + b.h - a.y) < eps : Math.abs(a.y + a.h - b.y) < eps;
      overlap = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x);
    }
    if (touches && overlap > bestScore + eps) {
      best = k;
      bestScore = overlap;
    }
  }
  return best;
}

/** New sizes for dragging a divider to position `at` (fraction of the pane area). */
export function dragSizes(l: Layout, d: Divider, at: number, min = 0.08): number[] {
  const n = node(l, d.path);
  if (isLeaf(n)) return [];
  const start = d.dir === "row" ? d.box.x : d.box.y;
  const span = d.dir === "row" ? d.box.w : d.box.h;
  const sizes = [...n.sizes];
  const before = sizes.slice(0, d.index).reduce((a, b) => a + b, 0);
  const pair = sizes[d.index] + sizes[d.index + 1];
  let first = (at - start) / span - before;
  first = Math.max(min, Math.min(pair - min, first));
  sizes[d.index] = first;
  sizes[d.index + 1] = pair - first;
  return sizes;
}
