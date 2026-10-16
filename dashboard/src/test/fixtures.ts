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
const folderConfig = {
  path: "/guide/.lore/config.yaml",
  name: "config.yaml",
  source: "rules:\n  frontmatter:\n    match: ['**/*.md']\n    use: frontmatter\n",
  content_type: "text/yaml",
  facts: { bytes: 60, lines: 4, characters: 60, tokens: 15 },
  binary: false,
};
const serverConfigs = [
  {
    path: "/opt/openlore/lore.json",
    name: "lore.json",
    source: '{\n  "docsets": {\n    "guide": { "paths": ["/guide"] }\n  }\n}\n',
    content_type: "application/json",
    facts: { bytes: 58, lines: 5, characters: 58, tokens: 20 },
    binary: false,
  },
  {
    path: "/opt/openlore/openlore.yml",
    name: "openlore.yml",
    source: "port: 2322\nhttp_port: 8080\nauth_file: ./lore.json\n",
    content_type: "text/yaml",
    facts: { bytes: 49, lines: 3, characters: 49, tokens: 14 },
    binary: false,
  },
];
const setup = {
  identity: "private@example.test",
  roles: ["writer"],
  home: "guide",
  access: false,
  config_edit: false,
  server: {
    ssh: "ssh lore@lore.example.test",
    http_url: "https://lore.example.test",
    mcp_url: "https://lore.example.test/mcp",
    lore_path: "/lore",
    readonly: false,
    skills: true,
    analytics: true,
    history: false,
  },
  docsets: [
    {
      name: "guide",
      roots: ["/guide"],
      aliases: [],
      grants: ["read", "write"],
      writable: true,
      readonly: false,
      home: true,
      inbox: "/guide/inbox",
      agent_skills: false,
      okf: true,
      config_files: [folderConfig.path],
    },
  ],
  config_files: [{ path: folderConfig.path, kind: "folder", scope: "/guide", docset: "guide" }],
  truncated: false,
  notes: [],
};
export function mockAPI(
  options: {
    contextError?: boolean;
    access?: boolean;
    partialContext?: boolean;
    configEdit?: boolean;
  } = {},
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
    if (path.endsWith("/files"))
      return json({
        path: url.searchParams.get("path"),
        entries: [
          { path: "/guide/other.md", name: "other.md" },
          { path: file.path, name: file.name },
          { path: "/notes/strategy.md", name: "strategy.md" },
        ],
        truncated: false,
      });
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
    if (path.endsWith("/file")) {
      const requested = url.searchParams.get("path");
      if (requested === file.path) return json(file);
      if (requested === folderConfig.path) return json(folderConfig);
      const server = serverConfigs.find((item) => item.path === requested);
      if (server && options.configEdit) return json(server);
      return json({ error: "not found" }, 404);
    }
    if (path.endsWith("/setup"))
      return json(
        options.configEdit
          ? {
              ...setup,
              config_edit: true,
              config_files: [
                { path: serverConfigs[0].path, kind: "auth", scope: "/" },
                { path: serverConfigs[1].path, kind: "server", scope: "/" },
                ...setup.config_files,
              ],
            }
          : setup,
      );
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
