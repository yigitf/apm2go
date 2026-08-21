import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The build output goes straight into the Go package that embeds it, so
// `make web && make build` produces a single binary with the UI inside.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../internal/api/dist",
    emptyOutDir: true,
  },
  server: {
    // During development the UI runs on its own port and proxies to a apm2go
    // started with `apm2go run`, so the API contract is exercised for real.
    // APM2GO_DEV_API points it somewhere else -- at the container in
    // docker-compose.yml, say, which does not listen on the default port.
    proxy: {
      "/api": process.env.APM2GO_DEV_API ?? "http://127.0.0.1:8080",
    },
  },
});
