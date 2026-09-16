import {
  storageKey,
  readPreferences,
  writePreferences,
  defaults,
} from "../storage";
import { resolveLorePath } from "../FileReader";

test("identity is hashed out of persistence keys and values contain no bodies", async () => {
  const identity = "secret-user@example.test",
    key = await storageKey(identity);
  expect(key).not.toContain(identity);
  writePreferences(key, { ...defaults, openPaths: ["/guide/start.md"] });
  expect(JSON.stringify(localStorage)).not.toContain(identity);
  expect(localStorage.getItem(key)).not.toContain("Hello document body");
});
test("relative Markdown resources resolve against the canonical file parent", () => {
  expect(resolveLorePath("../images/a b.png", "/guide/start.md")).toBe(
    "/images/a b.png",
  );
  expect(resolveLorePath("./next.md", "/guide/start.md")).toBe(
    "/guide/next.md",
  );
});

test("invalid stored schema cannot replace valid preferences or restore non-path data", () => {
  localStorage.setItem(
    "prefs",
    JSON.stringify({
      ratio: 0,
      contextWindow: -1,
      openPaths: [null, 5, "body", "/valid.md"],
      expandedPaths: "bad",
      positions: { "/valid.md": -2 },
      source: "PRIVATE BODY",
    }),
  );
  expect(readPreferences("prefs")).toEqual({
    ...defaults,
    openPaths: ["/valid.md"],
  });
});
