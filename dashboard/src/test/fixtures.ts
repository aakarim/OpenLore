import { vi } from "vitest";

const file = {
  path: "/guide/start.md",
  name: "start.md",
  source: "# Start\nHello",
  html: '<h1>Start</h1><p>Hello <a href="../other.md">other</a></p>',
  content_type: "text/markdown",
  facts: { bytes: 20, lines: 2, characters: 13, tokens: 4 },
  binary: false,
};
const context = {
  path: "/",
  name: "Workspace",
  directory: true,
  bytes: 1200,
  lines: 30,
  characters: 4000,
  tokens: 1000,
  children: [
    {
      path: "/guide",
      name: "guide",
      directory: true,
      bytes: 1200,
      lines: 30,
      characters: 4000,
      tokens: 1000,
      children: [
        {
          ...file.facts,
          path: "/guide/start.md",
          name: "start.md",
          directory: false,
        },
      ],
    },
  ],
};
const usage = {
  reads: 12,
  hits: 4,
  writes: 7,
  commands: 19,
  human_writes: 2,
  agent_writes: 4,
  unknown_writes: 1,
  estimated_tokens: 480,
  estimated_reads: 10,
  unestimated_reads: 2,
  computed_at: "2026-09-16T10:00:00Z",
  activity: [
    {
      date: "2026-09-15",
      human: 2,
      agent: 3,
      unknown: 1,
      reads: 12,
      writes: 6,
    },
  ],
};
export function mockAPI(
  options: { contextError?: boolean; access?: boolean; partialContext?: boolean } = {},
) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = new URL(String(input), location.origin),
      path = url.pathname;
    if (path.endsWith("/session"))
      return json({
        identity: "private@example.test",
        lore_path: "/lore",
        access: options.access || false,
      });
    if (path.endsWith("/tree")) {
      const scope = url.searchParams.get("path");
      if (scope === file.path) return json({ error: "not a folder" }, 404);
      return json(
        scope === "/"
          ? {
              path: "/",
              entries: [
                { path: "/guide", name: "guide", directory: true, bytes: 1200 },
              ],
            }
          : {
              path: "/guide",
              entries: [
                {
                  path: file.path,
                  name: file.name,
                  directory: false,
                  bytes: 20,
                },
              ],
            },
      );
    }
    if (path.endsWith("/context"))
      return options.contextError
        ? json({ error: "too large" }, 413)
        : json(
            url.searchParams.get("path") === file.path
              ? {
                  ...file.facts,
                  path: file.path,
                  name: file.name,
                  directory: false,
                }
              : options.partialContext
                ? {
                    ...context,
                    analytics: {
                      state: "ready",
                      updating: false,
                      complete: false,
                      coverage: "readable indexed content only; restricted docsets omitted",
                    },
                  }
                : context,
          );
    if (path.endsWith("/usage")) return json(usage);
    if (path.endsWith("/file"))
      return url.searchParams.get("path") === file.path
        ? json(file)
        : json({ error: "not found" }, 404);
    if (path.endsWith("/history"))
      return json({ available: true, entries: [] });
    if (path.includes("/analytics/aggregations/"))
      return json({
        status: "ok",
        table: {
          columns: ["path", "reads"],
          rows: [[file.path, 12]],
          total: 1,
        },
        computed_at: usage.computed_at,
        window: {},
      });
    return json({ error: `Unhandled ${path}` }, 404);
  });
}
function json(body: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}
