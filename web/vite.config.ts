import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  test: {
    environment: "jsdom",
    environmentOptions: {
      jsdom: {
        url: "https://120.25.210.202:9443/",
      },
    },
    globals: true,
  },
});
