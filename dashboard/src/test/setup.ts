import "@testing-library/jest-dom/vitest";
import { afterEach, vi } from "vitest";
import { cleanup } from "@testing-library/react";

Object.defineProperty(globalThis, "crypto", {
  value: {
    subtle: {
      digest: async (_: string, data: ArrayBuffer) =>
        new Uint8Array(32).map(
          (__, index) => new Uint8Array(data)[index % data.byteLength] || index,
        ).buffer,
    },
  },
});
Object.defineProperty(globalThis, "matchMedia", {
  value: vi.fn().mockImplementation((query) => ({
    matches: false,
    media: query,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })),
});
Object.defineProperty(navigator, "clipboard", {
  value: { writeText: vi.fn().mockResolvedValue(undefined) },
  configurable: true,
});
afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.restoreAllMocks();
  history.replaceState(null, "", "/dashboard/");
});
