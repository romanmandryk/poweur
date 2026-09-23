import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The build lands inside the Go module, which embeds it (bridge/web.go). The
// bridge serves /assets/* and fills index.html's page data per request.
export default defineConfig({
  base: "/",
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../bridge/web/dist",
    emptyOutDir: true,
    assetsDir: "assets",
  },
});
