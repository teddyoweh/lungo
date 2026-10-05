import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { writeFileSync } from "node:fs";

// dist/ is embedded by the Go build; keep a placeholder so `go build ./...` works on a
// fresh checkout before the frontend has been built.
const keepDist = (): Plugin => ({
  name: "keep-dist",
  closeBundle() {
    writeFileSync("dist/.gitkeep", "");
  },
});

export default defineConfig({
  plugins: [react(), tailwindcss(), keepDist()],
  server: { port: 5199, strictPort: false },
  build: { target: "es2022", chunkSizeWarningLimit: 2000 },
});
