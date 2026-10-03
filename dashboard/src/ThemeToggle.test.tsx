import { act, fireEvent, render, screen } from "@testing-library/react";
import { vi } from "vitest";
import { initializeTheme, ThemeToggle } from "./ThemeToggle";

test("follows live system changes until an explicit choice, then restores that choice", () => {
  const media = {
    matches: true,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  };
  vi.spyOn(window, "matchMedia").mockReturnValue(
    media as unknown as MediaQueryList,
  );
  expect(initializeTheme()).toBe("dark");
  const view = render(<ThemeToggle />);
  const change = media.addEventListener.mock.calls[0][1];
  media.matches = false;
  act(() => change());
  expect(document.documentElement.dataset.theme).toBe("light");
  fireEvent.click(screen.getByRole("button", { name: "Switch to dark mode" }));
  expect(document.documentElement.dataset.theme).toBe("dark");
  expect(localStorage.getItem("openlore-theme")).toBe("dark");
  expect(media.removeEventListener).toHaveBeenCalledWith("change", change);
  view.unmount();
  render(<ThemeToggle />);
  expect(
    screen.getByRole("button", { name: "Switch to light mode" }),
  ).toBeInTheDocument();
  expect(document.documentElement.dataset.theme).toBe("dark");
  fireEvent.click(screen.getByRole("button", { name: "Switch to light mode" }));
  expect(document.documentElement.dataset.theme).toBe("light");
  expect(localStorage.getItem("openlore-theme")).toBe("light");
});

test("stored light mode wins over a dark system and invalid preferences fall back", () => {
  vi.spyOn(window, "matchMedia").mockReturnValue({
    matches: true,
  } as MediaQueryList);
  localStorage.setItem("openlore-theme", "light");
  expect(initializeTheme()).toBe("light");
  localStorage.setItem("openlore-theme", "invalid");
  expect(initializeTheme()).toBe("dark");
});

test("theme switching works when browser storage is unavailable", () => {
  vi.spyOn(window, "matchMedia").mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  } as unknown as MediaQueryList);
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new Error("blocked");
  });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("blocked");
  });
  render(<ThemeToggle />);
  fireEvent.click(screen.getByRole("button", { name: "Switch to dark mode" }));
  expect(document.documentElement.dataset.theme).toBe("dark");
  expect(
    screen.getByRole("button", { name: "Switch to light mode" }),
  ).toBeInTheDocument();
});
