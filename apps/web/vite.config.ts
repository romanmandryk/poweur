import { defineConfig } from "vitest/config";
import react, { reactCompilerPreset } from "@vitejs/plugin-react";
import babel from "@rolldown/plugin-babel";
import tailwindcss from "@tailwindcss/vite";

// `base: "./"` keeps every asset reference relative, so one build works at
// /app/ on a relay, at / on a static server, and at capacitor://localhost/
// inside the EPIC-019 shell.
export default defineConfig({
  base: "./",
  plugins: [
    react(),
    babel({ presets: [reactCompilerPreset()] }),
    tailwindcss(),
  ],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: true,
  },
  test: {
    environment: "happy-dom",
    setupFiles: ["./test/setup.ts"],
    include: ["test/**/*.test.{ts,tsx,js}"],
    // Live-relay suites spawn `go run`; the first compile is slow.
    testTimeout: 120_000,
    hookTimeout: 120_000,
  },
});
