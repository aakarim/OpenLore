import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { readFileSync } from "node:fs";

const thirdPartyLicenses = readFileSync(
  new URL("./THIRD_PARTY_LICENSES.txt", import.meta.url),
  "utf8",
);

export default defineConfig({
  base: "/dashboard/",
  plugins: [
    react(),
    {
      name: "third-party-license-notice",
      generateBundle() {
        this.emitFile({
          type: "asset",
          fileName: "assets/THIRD_PARTY_LICENSES.txt",
          source: thirdPartyLicenses,
        });
      },
    },
  ],
  build: { outDir: "../assets/dashboard/dist", emptyOutDir: true },
  test: {
    environment: "jsdom",
    setupFiles: "./src/test/setup.ts",
    css: true,
    globals: true,
  },
});
