import { createRoot } from "react-dom/client";
import "@fontsource/inter/400.css";
import "@fontsource/inter/500.css";
import "@fontsource/inter/600.css";
import "@fontsource/inter/700.css";
import "@fontsource/jetbrains-mono/400.css";
import "@fontsource/jetbrains-mono/600.css";
import "@fontsource/jetbrains-mono/400-italic.css";
import "./index.css";
import { App } from "./App";
import * as store from "./lib/store";
import { applyTheme, getState } from "./lib/store";
import * as peek from "./lib/peek";
import * as screen from "./lib/screen";

applyTheme(getState().theme);
if (import.meta.env.DEV) (window as unknown as { __sky: unknown }).__sky = { getState, store, peek, screen }; // dev checks drive the store through this
createRoot(document.getElementById("root")!).render(<App />);
