import { defineConfig } from "vite";
import { readFileSync } from "node:fs";
import { renderWebsite } from "./src/website-template";

export default defineConfig({
  plugins: [{
    name: "website-languages",
    transformIndexHtml: {
      order: "pre",
      handler(html, context) {
        if (context.path.startsWith("/console/")) return html;
        const template = readFileSync(new URL("./index.html", import.meta.url), "utf8");
        return renderWebsite(template, context.path.startsWith("/zh/") ? "zh" : "en");
      },
    },
  }],
  build: {
    rollupOptions: {
      input: {
        website: new URL("./index.html", import.meta.url).pathname,
        english: new URL("./en/index.html", import.meta.url).pathname,
        chinese: new URL("./zh/index.html", import.meta.url).pathname,
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
