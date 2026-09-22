import { useCallback, useEffect, useState } from "react";
import { Analytics, analyticsTabs } from "./Analytics";
import { APIError, api } from "./api";
import { FileReader, MobileDetails, Sheet } from "./FileReader";
import { useAsync } from "./hooks";
import { SettingsIcon } from "./icons";
import {
  defaults,
  readPreferences,
  storageKey,
  writePreferences,
  type Preferences,
} from "./storage";
import { Tree } from "./Tree";
import type { AnalyticsTab, Session } from "./types";

type View = "analytics" | "files";
type Route = { view: View; path: string; tab: AnalyticsTab };
type SheetName =
  | "folders"
  | "open"
  | "details"
  | "analytics"
  | "settings"
  | null;
const name = (path: string) =>
  path.split("/").filter(Boolean).at(-1) || "Workspace";
const ancestors = (path: string) => [
  "/",
  ...path
    .split("/")
    .filter(Boolean)
    .slice(0, -1)
    .map((_, i, parts) => `/${parts.slice(0, i + 1).join("/")}`),
];

function readRoute(session: Session): Route {
  const query = new URLSearchParams(location.search);
  const tab =
    analyticsTabs.find((t) => t.id === query.get("tab"))?.id || "overview";
  const prefix = session.lore_path.replace(/\/$/, "");
  if (
    location.pathname === prefix ||
    location.pathname.startsWith(`${prefix}/`)
  ) {
    let path = location.pathname.slice(prefix.length) || "/";
    try {
      path = decodeURIComponent(path);
    } catch {
      /* show unavailable path */
    }
    return { view: "files", path, tab };
  }
  return {
    view: query.get("view") === "analytics" ? "analytics" : "files",
    path: query.get("path") || "/",
    tab,
  };
}

function routeURL(route: Route, session: Session) {
  if (route.view === "files" && route.path !== "/")
    return `${session.lore_path.replace(/\/$/, "")}/${route.path.split("/").filter(Boolean).map(encodeURIComponent).join("/")}`;
  return `/dashboard/?${new URLSearchParams(route)}`;
}

export function loginURL(value = "/passkey/login") {
  const url = new URL(value, location.origin);
  url.searchParams.set("redirect", `${location.pathname}${location.search}`);
  return url.toString();
}

export function App() {
  const session = useAsync((signal) => api.session(signal), [], true, true);
  useEffect(() => {
    const refresh = () => session.refresh();
    const visible = () => {
      if (document.visibilityState === "visible") refresh();
    };
    addEventListener("focus", refresh);
    addEventListener("dashboard-auth-expired", refresh);
    document.addEventListener("visibilitychange", visible);
    return () => {
      removeEventListener("focus", refresh);
      removeEventListener("dashboard-auth-expired", refresh);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [session.refresh]);
  if (session.error)
    return (
      <main className="startup">
        <div className="brand">
          <b>O</b> OpenLore
        </div>
        <h1>
          {session.error instanceof APIError && session.error.status === 401
            ? "Sign in to OpenLore"
            : "Dashboard unavailable"}
        </h1>
        <p>{session.error.message}</p>
        {session.error instanceof APIError && session.error.status === 401 ? (
          <a className="primary button" href={loginURL(session.error.loginURL)}>
            Continue to sign in
          </a>
        ) : (
          <button onClick={session.refresh}>Retry</button>
        )}
      </main>
    );
  if (!session.data)
    return (
      <main className="startup" role="status">
        Loading your workspace…
      </main>
    );
  return <Workspace key={session.data.identity} session={session.data} />;
}

function Workspace({ session }: { session: Session }) {
  const [route, setRoute] = useState(() => readRoute(session));
  const [prefs, setPrefs] = useState<Preferences>(defaults);
  const [prefsKey, setPrefsKey] = useState("");
  const [activeFile, setActiveFile] = useState("");
  const [fileLocation, setFileLocation] = useState("/");
  const [scope, setScope] = useState(route.path);
  const [ready, setReady] = useState(false);
  const [routeError, setRouteError] = useState("");
  const [sheet, setSheet] = useState<SheetName>(null);
  const [mode, setMode] = useState<"preview" | "source">("preview");
  const [collapsed, setCollapsed] = useState(false);
  const [days, setDays] = useState(30);
  const [computed, setComputed] = useState("");
  const [revision, setRevision] = useState(0);
  const [toast, setToast] = useState("");
  const setPreference = (patch: Partial<Preferences>) =>
    setPrefs((p) => ({ ...p, ...patch }));

  // One ordered restoration owns both saved tabs and direct URLs. Direct routes
  // win; persisted paths are never trusted as proof of current authorization.
  useEffect(() => {
    const controller = new AbortController();
    const signal = controller.signal;
    void (async () => {
      const key = await storageKey(session.identity);
      const stored = readPreferences(key);
      const valid = (
        await Promise.all(
          stored.openPaths.map(async (path) => {
            try {
              return (await api.file(path, signal)).path;
            } catch {
              return "";
            }
          }),
        )
      ).filter(Boolean);
      const next = readRoute(session);
      let file = "";
      let error = "";
      if (next.view === "files" && next.path !== "/") {
        try {
          file = (await api.file(next.path, signal)).path;
          next.path = file;
        } catch {
          try {
            next.path = (await api.tree(next.path, signal)).path;
          } catch {
            error = "This path is unavailable or no longer readable.";
          }
        }
      }
      if (signal.aborted) return;
      setPrefs({
        ...stored,
        openPaths: [...new Set([...valid, ...(file ? [file] : [])])],
        expandedPaths: [
          ...new Set([...stored.expandedPaths, ...ancestors(next.path)]),
        ],
      });
      setPrefsKey(key);
      setRoute(next);
      setScope(next.path);
      if (next.view === "files") setFileLocation(next.path);
      setActiveFile(file);
      setRouteError(error);
      setReady(true);
    })();
    return () => controller.abort();
  }, [session.identity, session.lore_path]);
  useEffect(() => {
    if (ready && prefsKey) writePreferences(prefsKey, prefs);
  }, [ready, prefsKey, prefs]);
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast(""), 3000);
    return () => clearTimeout(timer);
  }, [toast]);

  const navigate = useCallback(
    (view: View, path: string, tab: AnalyticsTab = route.tab) => {
      const next = { view, path, tab };
      history.pushState(null, "", routeURL(next, session));
      setRoute(next);
      setRouteError("");
      setSheet(null);
      if (view === "analytics") setScope(path);
      else setFileLocation(path);
      setPrefs((p) => ({
        ...p,
        expandedPaths: [...new Set([...p.expandedPaths, ...ancestors(path)])],
      }));
    },
    [route.tab, session.lore_path],
  );
  const openFile = useCallback(
    (path: string) => {
      setActiveFile(path);
      setPrefs((p) => ({
        ...p,
        openPaths: [...new Set([...p.openPaths, path])],
      }));
      navigate("files", path);
    },
    [navigate],
  );
  const openFolder = (path: string) => {
    if (route.view === "files") setActiveFile("");
    navigate(route.view, path);
  };
  const selectFile = (path: string) =>
    route.view === "analytics" ? navigate("analytics", path) : openFile(path);
  useEffect(() => {
    let controller: AbortController | undefined;
    const pop = async () => {
      controller?.abort();
      controller = new AbortController();
      const signal = controller.signal;
      const next = readRoute(session);
      setRoute(next);
      setRouteError("");
      setSheet(null);
      if (next.view === "analytics") {
        setScope(next.path);
        return;
      }
      setFileLocation(next.path);
      setActiveFile("");
      if (next.path === "/") return;
      try {
        const file = await api.file(next.path, signal);
        if (signal.aborted) return;
        setActiveFile(file.path);
        setPrefs((p) => ({
          ...p,
          openPaths: [...new Set([...p.openPaths, file.path])],
        }));
      } catch {
        try {
          await api.tree(next.path, signal);
        } catch {
          if (!signal.aborted)
            setRouteError("This path is unavailable or no longer readable.");
        }
      }
    };
    addEventListener("popstate", pop);
    return () => {
      controller?.abort();
      removeEventListener("popstate", pop);
    };
  }, [session.lore_path]);
  const copy = async (value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setToast("Copied");
    } catch {
      setToast("Clipboard unavailable. Select and copy the path instead.");
    }
  };
  const closeFile = (path: string) => {
    const remaining = prefs.openPaths.filter((p) => p !== path);
    setPreference({ openPaths: remaining });
    if (activeFile === path) {
      setActiveFile(remaining.at(-1) || "");
      navigate("files", remaining.at(-1) || "/");
    }
  };
  if (!ready)
    return (
      <main className="startup" role="status">
        Restoring workspace…
      </main>
    );
  const { view, path } = route;
  const tab =
    !session.access && route.tab === "access" ? "overview" : route.tab;
  const treeProps = {
    selected: view === "files" ? activeFile || path : path,
    expanded: prefs.expandedPaths,
    onExpanded: (expandedPaths: string[]) => setPreference({ expandedPaths }),
    onFile: selectFile,
    onFolder: openFolder,
  };
  return (
    <div className={`app ${view}${collapsed ? " tree-collapsed" : ""}`}>
      <header className="topbar">
        <button
          className="tree-toggle"
          aria-label={collapsed ? "Expand tree" : "Collapse tree"}
          onClick={() => setCollapsed(!collapsed)}
        >
          ☰
        </button>
        <div className="brand">
          <b>O</b>
          <span>OpenLore</span>
        </div>
        <nav aria-label="Workspace views">
          <button
            aria-current={view === "analytics"}
            onClick={() =>
              navigate(
                "analytics",
                view === "files" ? activeFile || path : scope,
                tab,
              )
            }
          >
            Analytics
          </button>
          <button
            aria-current={view === "files"}
            onClick={() => navigate("files", activeFile || fileLocation)}
          >
            Files{prefs.openPaths.length ? ` ${prefs.openPaths.length}` : ""}
          </button>
        </nav>
        <span className="identity">{session.identity}</span>
      </header>
      {!collapsed && <Tree {...treeProps} />}
      <main className="workspace">
        {routeError ? (
          <div className="state error" role="alert">
            {routeError}
            <button onClick={() => openFolder("/")}>Browse workspace</button>
          </div>
        ) : view === "analytics" ? (
          <>
            <div className="workspace-head">
              <div>
                <div className="breadcrumbs">
                  <button onClick={() => openFolder("/")}>Workspace</button>
                  {path
                    .split("/")
                    .filter(Boolean)
                    .map((part, i, parts) => (
                      <span key={i}>
                        /{" "}
                        <button
                          onClick={() =>
                            navigate(
                              "analytics",
                              `/${parts.slice(0, i + 1).join("/")}`,
                            )
                          }
                        >
                          {part}
                        </button>
                      </span>
                    ))}
                  <button
                    className="icon-button"
                    aria-label="Copy path"
                    title="Copy agent path"
                    onClick={() => void copy(path)}
                  >
                    □
                  </button>
                  <button
                    className="icon-button"
                    aria-label="Copy URL"
                    title="Copy URL"
                    onClick={() => void copy(location.href)}
                  >
                    ↗
                  </button>
                </div>
                <h1>{name(path)}</h1>
              </div>
              <div className="head-actions">
                <button
                  title={`Refresh analytics${computed ? `. Last computed ${new Date(computed).toLocaleString()}` : ""}`}
                  aria-label="Refresh analytics"
                  onClick={() => setRevision((r) => r + 1)}
                >
                  ↻<span>Refresh</span>
                </button>
                <button
                  aria-label="Settings"
                  onClick={() => setSheet("settings")}
                >
                  <SettingsIcon />
                  <span>Settings</span>
                </button>
              </div>
            </div>
            <Analytics
              key={revision}
              path={path}
              tab={tab}
              days={days}
              onDays={setDays}
              ratio={prefs.ratio}
              contextWindow={prefs.contextWindow}
              canAccess={session.access}
              onTab={(t) => navigate("analytics", path, t)}
              onScope={(p) => navigate("analytics", p, tab)}
              onFile={openFile}
              onComputed={setComputed}
            />
          </>
        ) : (
          <>
            <div className="file-tabs" role="tablist" aria-label="Open files">
              {prefs.openPaths.map((p) => (
                <div className={p === activeFile ? "active" : ""} key={p}>
                  <button
                    role="tab"
                    aria-selected={p === activeFile}
                    onClick={() => openFile(p)}
                  >
                    {name(p)}
                  </button>
                  <button
                    aria-label={`Close ${name(p)}`}
                    onClick={() => closeFile(p)}
                  >
                    ×
                  </button>
                </div>
              ))}
            </div>
            {activeFile ? (
              <FileReader
                key={activeFile}
                path={activeFile}
                session={session}
                mode={mode}
                onMode={setMode}
                onFile={openFile}
                onAnalytics={() => navigate("analytics", activeFile, "usage")}
                contextWindow={prefs.contextWindow}
                ratio={prefs.ratio}
                scrollPosition={prefs.positions[activeFile] || 0}
                onScroll={(position) =>
                  setPrefs((p) => ({
                    ...p,
                    positions: { ...p.positions, [activeFile]: position },
                  }))
                }
              />
            ) : (
              <FolderBrowser
                path={path}
                onFile={openFile}
                onFolder={openFolder}
              />
            )}
          </>
        )}
      </main>
      <nav className="mobile-nav" aria-label="Mobile workspace">
        <button onClick={() => setSheet("folders")}>▱ Folders</button>
        {view === "files" && (
          <button onClick={() => setSheet("open")}>
            Open files · {prefs.openPaths.length} ⌃
          </button>
        )}
        <button
          disabled={view === "files" && !activeFile}
          onClick={() => setSheet(view === "files" ? "details" : "analytics")}
        >
          {view === "files"
            ? "ⓘ Details"
            : `${analyticsTabs.find((t) => t.id === tab)?.glyph} ${analyticsTabs.find((t) => t.id === tab)?.label}`}{" "}
          ⌃
        </button>
      </nav>
      {sheet === "folders" && (
        <Sheet title="Folders" onClose={() => setSheet(null)}>
          <div className="sheet-tree">
            <Tree {...treeProps} onFolder={undefined} />
          </div>
        </Sheet>
      )}
      {sheet === "open" && (
        <Sheet title="Open files" onClose={() => setSheet(null)}>
          <div className="open-list">
            {prefs.openPaths.length ? (
              prefs.openPaths.map((p) => (
                <div key={p}>
                  <button
                    aria-current={p === activeFile}
                    onClick={() => openFile(p)}
                  >
                    <strong>{name(p)}</strong>
                    <small>{p}</small>
                  </button>
                  <button
                    aria-label={`Close ${name(p)}`}
                    onClick={() => closeFile(p)}
                  >
                    ×
                  </button>
                </div>
              ))
            ) : (
              <p className="empty">No open files. Choose one in Folders.</p>
            )}
          </div>
        </Sheet>
      )}
      {sheet === "details" && activeFile && (
        <MobileDetails
          path={activeFile}
          session={session}
          ratio={prefs.ratio}
          contextWindow={prefs.contextWindow}
          mode={mode}
          onMode={setMode}
          onClose={() => setSheet(null)}
          onAnalytics={() => navigate("analytics", activeFile, "usage")}
          onSettings={() => setSheet("settings")}
        />
      )}
      {sheet === "analytics" && (
        <Sheet title="Analytics sections" onClose={() => setSheet(null)}>
          <div className="analytics-picker">
            {analyticsTabs
              .filter((t) => t.id !== "access" || session.access)
              .map((t) => (
                <button
                  key={t.id}
                  className={t.id === tab ? "selected" : ""}
                  onClick={() => navigate("analytics", path, t.id)}
                >
                  <span>
                    {t.glyph} {t.label}
                  </span>
                  {t.id === tab && "✓"}
                </button>
              ))}
          </div>
        </Sheet>
      )}
      {sheet === "settings" && (
        <Sheet title="Preferences" onClose={() => setSheet(null)}>
          <div className="settings">
            <label>
              Characters per token
              <select
                value={prefs.ratio}
                onChange={(e) =>
                  setPreference({ ratio: Number(e.target.value) as 4 | 6 })
                }
              >
                <option value="4">4 characters</option>
                <option value="6">6 characters</option>
              </select>
            </label>
            <p>
              Estimates current context and cumulative recorded input. Does not
              change the server tokenizer or recorded telemetry.
            </p>
            <label>
              Context window
              <select
                value={prefs.contextWindow}
                onChange={(e) =>
                  setPreference({ contextWindow: Number(e.target.value) })
                }
              >
                <option value="128000">128,000 tokens</option>
                <option value="200000">200,000 tokens</option>
                <option value="1000000">1,000,000 tokens</option>
              </select>
            </label>
            <p>A comparison target only, not a model billing estimate.</p>
          </div>
        </Sheet>
      )}
      {toast && (
        <div className="toast" role="status">
          {toast}
        </div>
      )}
    </div>
  );
}

function FolderBrowser({
  path,
  onFile,
  onFolder,
}: {
  path: string;
  onFile: (path: string) => void;
  onFolder: (path: string) => void;
}) {
  const state = useAsync((signal) => api.tree(path, signal), [path]);
  if (state.loading)
    return <div className="state loading">Loading folder…</div>;
  if (state.error)
    return (
      <div className="state error">
        <p>{state.error.message}</p>
        <button onClick={state.refresh}>Retry</button>
      </div>
    );
  return (
    <section className="folder-browser">
      <h2>{name(path)}</h2>
      <p>{state.data?.entries.length || 0} items</p>
      {path !== "/" && (
        <button
          className="folder-up"
          onClick={() => onFolder(path.slice(0, path.lastIndexOf("/")) || "/")}
        >
          ↑ Parent folder
        </button>
      )}
      <div className="folder-list">
        {state.data?.entries.map((entry) => (
          <button
            key={entry.path}
            onClick={() =>
              entry.directory ? onFolder(entry.path) : onFile(entry.path)
            }
          >
            <span>{entry.directory ? "▱" : "≡"}</span>
            <strong>{entry.name}</strong>
            <small>
              {entry.directory
                ? "Folder"
                : `${entry.bytes.toLocaleString()} bytes`}
            </small>
          </button>
        ))}
      </div>
    </section>
  );
}
