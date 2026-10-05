// The icons of the sidebar's page row, drawn for it: one 24-unit grid, one stroke weight,
// round ends. Each has an outline form and a filled form; the page you are on shows filled,
// the way a Mac's tab bars and sidebars mark where you are. Details on a filled shape are cut
// out of it (a mask), so they show whatever is behind the icon.
import { useId, type ReactNode } from "react";

export type NavIconName = "home" | "sessions" | "machines" | "sync" | "keys" | "folders" | "accounts" | "settings" | "sidebar";

const STROKE = 1.7;

/** A filled shape with details cut out of it. */
function Cut({ shape, cuts }: { shape: ReactNode; cuts: ReactNode }) {
  const id = useId();
  return (
    <>
      <mask id={id} maskUnits="userSpaceOnUse" x="0" y="0" width="24" height="24">
        <rect width="24" height="24" fill="white" />
        <g fill="black" stroke="black" strokeWidth={STROKE + 0.3} strokeLinecap="round" strokeLinejoin="round">
          {cuts}
        </g>
      </mask>
      <g mask={`url(#${id})`} fill="currentColor" stroke="currentColor" strokeWidth={STROKE} strokeLinejoin="round">
        {shape}
      </g>
    </>
  );
}

const draw: Record<NavIconName, (filled: boolean) => ReactNode> = {
  // A house with its door.
  home: (filled) => {
    const house = <path d="M4 10.4 12 4.1l8 6.3v8.4c0 .9-.7 1.6-1.6 1.6H5.6c-.9 0-1.6-.7-1.6-1.6z" />;
    const door = <path d="M10 20.4v-5.2c0-.5.4-.9.9-.9h2.2c.5 0 .9.4.9.9v5.2" />;
    return filled ? (
      <Cut shape={house} cuts={door} />
    ) : (
      <>
        {house}
        {door}
      </>
    );
  },
  // A terminal window with a prompt and its cursor.
  sessions: (filled) => {
    const box = <rect x="3" y="4.5" width="18" height="15" rx="3.75" />;
    const prompt = (
      <>
        <path d="M7.6 9.6 10.4 12l-2.8 2.4" fill="none" />
        <path d="M13 14.6h3.6" fill="none" />
      </>
    );
    return filled ? (
      <Cut shape={box} cuts={prompt} />
    ) : (
      <>
        {box}
        {prompt}
      </>
    );
  },
  // Two machines in a rack, each with its light.
  machines: (filled) => {
    const units = (
      <>
        <rect x="3.5" y="3.75" width="17" height="7.25" rx="2.4" />
        <rect x="3.5" y="13" width="17" height="7.25" rx="2.4" />
      </>
    );
    const lights = (
      <>
        <circle cx="7.4" cy="7.4" r="0.55" />
        <circle cx="7.4" cy="16.6" r="0.55" />
      </>
    );
    return filled ? (
      <Cut shape={units} cuts={lights} />
    ) : (
      <>
        {units}
        <g fill="currentColor">{lights}</g>
      </>
    );
  },
  // Two arrows chasing each other round.
  sync: (filled) => (
    <g strokeWidth={filled ? STROKE + 0.45 : STROKE}>
      <path d="M19.4 10.6A7.6 7.6 0 0 0 6.2 7" />
      <path d="M5.8 3.6V7.3h3.7" />
      <path d="M4.6 13.4A7.6 7.6 0 0 0 17.8 17" />
      <path d="M18.2 20.4v-3.7h-3.7" />
    </g>
  ),
  // A key: its bow, the blade, two teeth.
  keys: (filled) => {
    const bow = <circle cx="8" cy="15.9" r="4.1" />;
    const blade = (
      <g fill="none" strokeLinecap="round">
        <path d="M10.95 12.95 19.5 4.4" />
        <path d="M15.8 8.1l2.4 2.4" />
        <path d="M18 5.9l1.9 1.9" />
      </g>
    );
    return filled ? (
      <>
        <Cut shape={bow} cuts={<circle cx="7.4" cy="16.5" r="0.9" />} />
        <g stroke="currentColor" strokeWidth={STROKE + 0.2}>
          {blade}
        </g>
      </>
    ) : (
      <>
        {bow}
        {blade}
      </>
    );
  },
  // A folder.
  folders: (filled) => {
    const folder = <path d="M3.4 7.4c0-1.2.9-2.1 2.1-2.1h3.4c.6 0 1.1.25 1.5.7l1.2 1.4h6.9c1.2 0 2.1.9 2.1 2.1v8c0 1.2-.9 2.1-2.1 2.1H5.5c-1.2 0-2.1-.9-2.1-2.1z" />;
    return filled ? (
      <Cut shape={folder} cuts={<path d="M3.6 10.3h16.8" fill="none" />} />
    ) : (
      <>
        {folder}
        <path d="M3.6 10.3h16.8" />
      </>
    );
  },
  // A person.
  accounts: (filled) =>
    filled ? (
      <g fill="currentColor" stroke="currentColor" strokeWidth={STROKE}>
        <circle cx="12" cy="8.3" r="3.6" />
        <path d="M5.1 19.4c.7-3.3 3.5-5.4 6.9-5.4s6.2 2.1 6.9 5.4c.1.5-.3.9-.8.9H5.9c-.5 0-.9-.4-.8-.9z" />
      </g>
    ) : (
      <>
        <circle cx="12" cy="8.3" r="3.6" />
        <path d="M5.1 19.4c.7-3.3 3.5-5.4 6.9-5.4s6.2 2.1 6.9 5.4c.1.5-.3.9-.8.9H5.9c-.5 0-.9-.4-.8-.9z" />
      </>
    ),
  // Two sliders.
  settings: (filled) => (
    <>
      <path d="M4 7.5h7.4M17.6 7.5H20M4 16.5h2.4M12.6 16.5H20" />
      <circle cx="14.5" cy="7.5" r="2.6" fill={filled ? "currentColor" : "none"} />
      <circle cx="9.5" cy="16.5" r="2.6" fill={filled ? "currentColor" : "none"} />
    </>
  ),
  // A window with its sidebar.
  sidebar: (filled) => (
    <>
      <rect x="3" y="4.5" width="18" height="15" rx="3.75" />
      {filled ? <path d="M3.9 6.6c0-.9.8-1.6 1.7-1.6H9v14H5.6c-.9 0-1.7-.7-1.7-1.6z" fill="currentColor" stroke="none" /> : <path d="M9.25 5v14" />}
    </>
  ),
};

export function NavIcon({ name, filled = false, size = 18 }: { name: NavIconName; filled?: boolean; size?: number }) {
  return (
    <svg viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="currentColor" strokeWidth={STROKE} strokeLinecap="round" strokeLinejoin="round" aria-hidden className="shrink-0">
      {draw[name](filled)}
    </svg>
  );
}
