import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

// The build lands in ../internal/webui/dist, which the Go binary embeds
// (internal/webui/web.go).
//
// There is deliberately no dev-server proxy: `vite dev` serves index.html
// verbatim, so the hosted prukka-api-base marker stays in place and every API
// and data-plane call goes straight to the daemon's own origin — a relative
// /api path never reaches this server. For `npm run dev` against a local
// daemon, allowlist the dev origin in the daemon config (daemon.cors_origin),
// or use `make dev`.
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  // Relative asset paths: the daemon serves the app under /ui/, and the
  // hosted deployment may serve it from any path.
  base: "./",
  // The brand assets are the public files: prukka.svg becomes the favicon
  // straight from its single source of truth in assets/brand. Every file in
  // that directory is copied into the bundle the Go binary embeds, so it
  // holds brand artwork and nothing else.
  publicDir: "../assets/brand",
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
});
