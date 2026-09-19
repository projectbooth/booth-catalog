/// <reference types="vitest/config" />
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Two personalities, one config (ADR 0030) — the same arrangement as booth-storage:
//   - `vite`/`vite dev` (command "serve"): the dev harness — index.html/src/main.tsx wraps
//     CatalogApp in a mock shell (src/devshell/DevShell.tsx), since booth-design's real shell
//     doesn't run here.
//   - `vite build` (command "build"): builds the publishable library (@projectbooth/catalog-ui)
//     from src/index.ts, external-izing react/react-dom so booth-design's own copies are used
//     (two React copies in one page is a classic cause of "Invalid hook call").
export default defineConfig(({ command }) => ({
  plugins: [react()],
  build:
    command === "build"
      ? {
          lib: {
            entry: fileURLToPath(new URL("./src/index.ts", import.meta.url)),
            formats: ["es"],
            fileName: "index",
          },
          rollupOptions: {
            external: ["react", "react-dom", "react/jsx-runtime"],
          },
        }
      : undefined,
  server: {
    proxy: {
      // Local dev only: booth-core's gateway would normally proxy /modules/catalog/* to this
      // service's own /api/* routes (and /modules/storage/* to booth-storage, which the
      // location picker calls). Pointed at BOOTH_CATALOG_DEV_BACKEND / BOOTH_STORAGE_DEV_BACKEND
      // when set, so `npm run dev` can hit locally running Go backends without CORS juggling.
      "/modules/catalog": {
        target: process.env.BOOTH_CATALOG_DEV_BACKEND ?? "http://localhost:8080",
        changeOrigin: true,
        // Mimics the gateway's prefix-stripping: /modules/catalog/api/datasets reaches the
        // backend as /api/datasets.
        rewrite: (path) => path.replace(/^\/modules\/catalog/, ""),
      },
      "/modules/storage": {
        target: process.env.BOOTH_STORAGE_DEV_BACKEND ?? "http://localhost:8081",
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/modules\/storage/, ""),
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/setupTests.ts"],
  },
}));
