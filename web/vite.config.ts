import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

// The build lands in ../internal/webui/dist, which the Go binary embeds
// (internal/webui/web.go).
// During `vite dev`, API and data-plane calls proxy to a local daemon.
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  // Relative asset paths: the daemon serves the app under /ui/, and the
  // hosted deployment may serve it from any path.
  base: "./",
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    // One JS + one CSS file, stable names: the output is embedded and
    // diffed in git, so hashed filenames would churn on every build.
    rollupOptions: {
      output: {
        entryFileNames: "app.js",
        assetFileNames: "[name][extname]",
      },
    },
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8080",
    },
  },
});
