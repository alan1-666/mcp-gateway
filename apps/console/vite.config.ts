import { defineConfig } from "vite";

export default defineConfig({
  build: {
    rollupOptions: {
      input: {
        website: new URL("./index.html", import.meta.url).pathname,
        console: new URL("./console/index.html", import.meta.url).pathname,
      },
    },
  },
  server: {
    proxy: {
      "/api": { target: "http://127.0.0.1:8090", changeOrigin: true },
      "/mcp": { target: "http://127.0.0.1:8091", changeOrigin: true },
    },
  },
});
