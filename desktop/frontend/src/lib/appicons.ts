import clay from "../assets/app-icons/clay.png";
import crystal from "../assets/app-icons/crystal.png";
import dawn from "../assets/app-icons/dawn.png";
import keycap from "../assets/app-icons/keycap.png";
import lcd from "../assets/app-icons/lcd.png";
import phosphor from "../assets/app-icons/phosphor.png";
import pour from "../assets/app-icons/pour.png";
import pressed from "../assets/app-icons/pressed.png";
import redeye from "../assets/app-icons/redeye.png";
import sugar from "../assets/app-icons/sugar.png";

/** The app icons to pick from in Settings, in the order Go lists them (appicon.go). Redeye is the bundle's own. */
export const APP_ICONS = [
  { id: "redeye", name: "Redeye", src: redeye },
  { id: "dawn", name: "Dawn", src: dawn },
  { id: "phosphor", name: "Phosphor", src: phosphor },
  { id: "pour", name: "Pour", src: pour },
  { id: "keycap", name: "Keycap", src: keycap },
  { id: "pressed", name: "Pressed", src: pressed },
  { id: "sugar", name: "Sugar", src: sugar },
  { id: "lcd", name: "LCD", src: lcd },
  { id: "clay", name: "Clay", src: clay },
  { id: "crystal", name: "Crystal", src: crystal },
] as const;

export function appIconSrc(id: string | undefined): string {
  return (APP_ICONS.find((i) => i.id === id) ?? APP_ICONS[0]).src;
}
