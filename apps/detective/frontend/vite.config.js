import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  base: "./",
  build: { target: "safari15", sourcemap: false },
  test: {
    environment: "jsdom",
    setupFiles: "./src/test-setup.js",
    restoreMocks: true,
  },
});
