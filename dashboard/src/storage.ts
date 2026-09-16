export type Preferences = {
  ratio: 4 | 6;
  contextWindow: number;
  openPaths: string[];
  expandedPaths: string[];
  positions: Record<string, number>;
};
export const defaults: Preferences = {
  ratio: 4,
  contextWindow: 200000,
  openPaths: [],
  expandedPaths: ["/"],
  positions: {},
};

export async function storageKey(identity: string): Promise<string> {
  // In insecure HTTP contexts WebCrypto is unavailable. Prefer no persistence
  // over putting a raw identity into storage or preventing read-only browsing.
  if (!crypto.subtle) return "";
  const bytes = new TextEncoder().encode(`${location.origin}\0${identity}`);
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return `openlore:dashboard:${Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("")}`;
}
export function readPreferences(key: string): Preferences {
  if (!key) return defaults;
  try {
    const value = JSON.parse(localStorage.getItem(key) || "{}");
    const paths = (items: unknown): string[] =>
      Array.isArray(items)
        ? [
            ...new Set(
              items.filter(
                (p): p is string =>
                  typeof p === "string" && p.startsWith("/") && p.length < 4096,
              ),
            ),
          ].slice(0, 100)
        : [];
    const openPaths = paths(value.openPaths);
    return {
      ratio: value.ratio === 6 ? 6 : 4,
      contextWindow: [128000, 200000, 1000000].includes(value.contextWindow)
        ? value.contextWindow
        : defaults.contextWindow,
      openPaths,
      expandedPaths: [...new Set(["/", ...paths(value.expandedPaths)])],
      positions: Object.fromEntries(
        openPaths.flatMap((p) =>
          Number.isFinite(value.positions?.[p]) && value.positions[p] >= 0
            ? [[p, value.positions[p]]]
            : [],
        ),
      ),
    };
  } catch {
    return defaults;
  }
}
export function writePreferences(key: string, value: Preferences) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* Storage is optional (private browsing/quota). */
  }
}
