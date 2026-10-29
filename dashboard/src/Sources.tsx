import { useState } from "react";
import { api } from "./api";
import { useAsync, type AsyncState } from "./hooks";
import { FileIcon, FolderIcon, OverviewIcon } from "./icons";
import type { Source } from "./types";

export type SourcesState = AsyncState<Source[]> & { refresh: () => void };
const label = (source: Source) =>
  source.latest.meta?.name || source.store.split("/").at(-1);
const time = (value?: string) =>
  value && !value.startsWith("0001-")
    ? new Date(value).toLocaleString()
    : "Not synced yet";

export function SourcesTree({
  sources,
  selected,
  onSelect,
}: {
  sources: SourcesState;
  selected: string;
  onSelect: (store: string) => void;
}) {
  return (
    <aside className="sidebar" aria-label="Sources tree">
      <div className="sidebar-title">
        <span>SOURCES</span>
      </div>
      <button
        className={`tree-row ${selected === "/" ? "selected" : ""}`}
        aria-current={selected === "/" ? "page" : undefined}
        onClick={() => onSelect("/")}
      >
        <span className="chevron" />
        <OverviewIcon />
        <span>All sources</span>
      </button>
      {sources.data?.map((source) => (
        <button
          key={source.store}
          className={`tree-row ${selected === source.store ? "selected" : ""}`}
          aria-current={selected === source.store ? "page" : undefined}
          title={source.store}
          onClick={() => onSelect(source.store)}
        >
          <span className="chevron" />
          <FolderIcon />
          <span>{label(source)}</span>
        </button>
      ))}
    </aside>
  );
}

export function SourcesView({
  sources,
  store,
  onSelect,
}: {
  sources: SourcesState;
  store: string;
  onSelect: (store: string) => void;
}) {
  const source = sources.data?.find((s) => s.store === store);
  return (
    <>
      <div className="workspace-head">
        <div>
          <div className="breadcrumbs">
            <button onClick={() => onSelect("/")}>Sources</button>
            {source && <span> / {label(source)}</span>}
          </div>
          <h1>{source ? label(source) : "Sources"}</h1>
        </div>
        <button onClick={sources.refresh}>Refresh</button>
      </div>
      {sources.error ? (
        <div className="state error" role="alert">
          {sources.error.message}
          <button onClick={sources.refresh}>Retry</button>
        </div>
      ) : !sources.data ? (
        <p className="state" role="status">
          Loading sources…
        </p>
      ) : source ? (
        <SourceDetail key={source.store} source={source} />
      ) : store !== "/" ? (
        <p className="state">
          This source is unavailable or no longer readable.
        </p>
      ) : (
        <section className="source-content">
          <p className="coverage-note">
            Versioned source copies. Add, sync, and remove sources with the{" "}
            <code>lore</code> CLI. Originals are never modified.
          </p>
          {sources.data.length === 0 ? (
            <div className="card">
              <h2>No sources yet</h2>
              <p>
                Run <code>lore source add &lt;path&gt;</code>, then{" "}
                <code>lore sync</code>.
              </p>
            </div>
          ) : (
            <div className="data-table">
              <table>
                <thead>
                  <tr>
                    <th>Source</th>
                    <th>Owner</th>
                    <th>Projection</th>
                    <th>Files</th>
                    <th>Last snapshot</th>
                  </tr>
                </thead>
                <tbody>
                  {sources.data.map((s) => (
                    <tr key={s.store}>
                      <td>
                        <button onClick={() => onSelect(s.store)}>
                          {label(s)}
                        </button>
                        <small className="source-path">{s.store}</small>
                      </td>
                      <td>{s.owner}</td>
                      <td>{s.projections.join(", ") || "Not projected"}</td>
                      <td>{Object.keys(s.latest.files ?? {}).length}</td>
                      <td>{time(s.latest.time)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}
    </>
  );
}

function SourceDetail({ source }: { source: Source }) {
  const [tab, setTab] = useState("Overview");
  const [file, setFile] = useState("");
  const [filter, setFilter] = useState("");
  const history = useAsync(
    (signal) => api.sourceHistory(source.store, signal),
    [source.store, source.latest.snapshot],
    tab === "Activity",
  );
  const content = useAsync(
    (signal) => api.sourceFile(source.store, file, signal),
    [source.store, file, source.latest.snapshot],
    tab === "Files" && !!file,
  );
  const files = Object.entries(source.latest.files ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .filter(([name]) => name.toLowerCase().includes(filter.toLowerCase()));
  return (
    <section className="source-content">
      <p className="source-readonly">Source copy · read-only</p>
      <nav className="source-tabs" aria-label="Source sections">
        {["Overview", "Files", "Activity"].map((t) => (
          <button
            key={t}
            aria-current={tab === t ? "page" : undefined}
            onClick={() => setTab(t)}
          >
            {t}
          </button>
        ))}
      </nav>
      {tab === "Overview" && (
        <div className="card">
          <h2>Source overview</h2>
          <dl className="setup-facts">
            <div>
              <dt>Store path</dt>
              <dd>
                <code>{source.store}</code>
              </dd>
            </div>
            <div>
              <dt>Origin</dt>
              <dd>{source.latest.meta?.origin || "Not recorded"}</dd>
            </div>
            <div>
              <dt>Owner</dt>
              <dd>{source.owner}</dd>
            </div>
            <div>
              <dt>Projection</dt>
              <dd>
                {source.projections.join(", ") ||
                  "Not projected — not visible in the served filesystem"}
              </dd>
            </div>
            <div>
              <dt>Last snapshot</dt>
              <dd>
                {time(source.latest.time)}
                <code className="source-path">
                  {source.latest.snapshot || "—"}
                </code>
              </dd>
            </div>
            <div>
              <dt>Files</dt>
              <dd>{Object.keys(source.latest.files ?? {}).length}</dd>
            </div>
          </dl>
          <p>
            Refresh this copy with <code>lore sync</code>. Changes here cannot
            be written back to the original.
          </p>
        </div>
      )}
      {tab === "Files" && (
        <div className="source-browser">
          <div className="card source-file-list">
            <label>
              Find a source file
              <input
                type="search"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder="Filter paths…"
              />
            </label>
            <ul>
              {files.map(([name, f]) => (
                <li key={name}>
                  <button
                    aria-current={file === name ? "page" : undefined}
                    onClick={() => setFile(name)}
                  >
                    <FileIcon />
                    <span>
                      {name}
                      <small>{f.size.toLocaleString()} bytes</small>
                    </span>
                  </button>
                </li>
              ))}
            </ul>
            {!files.length && <p>No files match.</p>}
          </div>
          <div className="card source-file-preview">
            <h2>{file || "Select a file"}</h2>
            {file && (
              <p className="coverage-note">
                Latest snapshot · {source.latest.files[file]?.sha256}
              </p>
            )}
            {content.error ? (
              <p role="alert">{content.error.message}</p>
            ) : content.loading ? (
              <p role="status">Loading source copy…</p>
            ) : content.data !== undefined ? (
              <pre>{content.data.slice(0, 262144)}</pre>
            ) : (
              <p>Choose a path to read its latest synced content.</p>
            )}
            {content.data && content.data.length > 262144 && (
              <p>Preview truncated to 256 Ki characters.</p>
            )}
          </div>
        </div>
      )}
      {tab === "Activity" &&
        (history.error ? (
          <p role="alert">{history.error.message}</p>
        ) : !history.data ? (
          <p role="status">Loading snapshots…</p>
        ) : (
          <div className="data-table">
            <table>
              <thead>
                <tr>
                  <th>Snapshot</th>
                  <th>Time</th>
                  <th>Files</th>
                  <th>Deleted</th>
                </tr>
              </thead>
              <tbody>
                {[...history.data].reverse().map((s) => (
                  <tr key={s.snapshot}>
                    <td>
                      <code title={s.snapshot}>{s.snapshot.slice(0, 16)}</code>
                    </td>
                    <td>{time(s.time)}</td>
                    <td>{Object.keys(s.files ?? {}).length}</td>
                    <td>{s.deleted}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!history.data.length && <p>No committed snapshots.</p>}
          </div>
        ))}
    </section>
  );
}
