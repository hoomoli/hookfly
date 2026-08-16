import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": new URL("./src", import.meta.url).pathname } },
  server: { proxy: { "/api": "http://localhost:8081" } },
  test: {
    environment: "jsdom",
    exclude: ["e2e/**", "**/node_modules/**", "**/dist/**"],
    globals: true,
    setupFiles: "./src/test-setup.ts",
  },
});
