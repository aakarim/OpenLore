import { useEffect, useState } from "react";
import { MoonIcon, SunIcon } from "./icons";

type Theme = "light" | "dark";
const key = "openlore-theme";
function savedTheme(): Theme | null {
  try {
    const value = localStorage.getItem(key);
    return value === "light" || value === "dark" ? value : null;
  } catch {
    return null;
  }
}
export function initializeTheme(): Theme {
  const theme =
    savedTheme() ||
    (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  document.documentElement.dataset.theme = theme;
  return theme;
}

export function ThemeToggle() {
  const [preference, setPreference] = useState(savedTheme);
  const [theme, setTheme] = useState(initializeTheme);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);
  useEffect(() => {
    if (preference) return;
    const media = matchMedia("(prefers-color-scheme: dark)");
    const change = () => setTheme(media.matches ? "dark" : "light");
    change();
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, [preference]);
  return (
    <button
      className="theme-toggle"
      aria-label={`Switch to ${theme === "dark" ? "light" : "dark"} mode`}
      title={`Switch to ${theme === "dark" ? "light" : "dark"} mode`}
      onClick={() => {
        const next = theme === "dark" ? "light" : "dark";
        setPreference(next);
        setTheme(next);
        try {
          localStorage.setItem(key, next);
        } catch {
          /* Keep the choice for this session. */
        }
      }}
    >
      {theme === "dark" ? <SunIcon /> : <MoonIcon />}
    </button>
  );
}
