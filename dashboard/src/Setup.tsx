import type { ReactNode } from "react";
import { api } from "./api";
import { useAsync, type AsyncState } from "./hooks";
import {
  CopyIcon,
  FileIcon,
  FolderIcon,
  OverviewIcon,
  ShieldIcon,
} from "./icons";
import type { ConfigFile, RuleSpec, Setup, SetupDocset } from "./types";

export const docsURL = "https://openlore.sh/docs";

export type SetupState = AsyncState<Setup> & { refresh: () => void };
/** Loads setup once the view is first shown and keeps it across view changes. */
export function useSetup(enabled: boolean): SetupState {
  return useAsync((signal) => api.setup(signal), [], enabled, true);
}

const fileName = (path: string) => path.split("/").filter(Boolean).at(-1) || "";
/** The folder a config file governs, shown relative to its docset root. */
function scopeLabel(file: ConfigFile, docset?: SetupDocset) {
  const root = docset?.roots.find(
    (candidate) =>
      file.scope === candidate || file.scope.startsWith(`${candidate}/`),
  );
  if (!root) return file.scope;
  const relative = file.scope.slice(root.length).replace(/^\//, "");
  return relative ? `${relative}/` : "docset root";
}
function rulesSummary(rules: Record<string, RuleSpec> | undefined) {
  return Object.entries(rules ?? {}).sort(([a], [b]) => a.localeCompare(b));
}
/** Server-wide files mounted under /opt/openlore, in display order. */
const serverFiles = (files: ConfigFile[]) =>
  files.filter((file) => file.kind === "auth" || file.kind === "server");
const serverFileSummary: Record<ConfigFile["kind"], string> = {
  auth: "docsets, roles, identities and server-wide rules",
  server: "ports, transports, plugins and other deployment settings",
  folder: "",
};

/**
 * The configuration tree replaces the knowledge tree on the Setup view. It
 * lists the live lore.json and openlore.yml when the session may edit
 * configuration, then each docset's .lore/config.yaml files by governed folder.
 */
export function SetupTree({
  setup,
  selected,
  onSelect,
}: {
  setup: SetupState;
  selected: string;
  onSelect: (path: string) => void;
}) {
  const data = setup.data;
  const server = serverFiles(data?.config_files ?? []);
  const row = (path: string, label: string, icon: ReactNode, depth = 0) => (
    <button
      key={path}
      className={`tree-row ${selected === path ? "selected" : ""}`}
      style={{ paddingLeft: 10 + depth * 16 }}
      aria-current={selected === path ? "page" : undefined}
      onClick={() => onSelect(path)}
    >
      <span className="chevron" />
      {icon}
      <span>{label}</span>
    </button>
  );
  return (
    <aside className="sidebar" aria-label="Configuration tree">
      <div className="sidebar-title">
        <span>CONFIGURATION</span>
      </div>
      {row("/", "Overview", <OverviewIcon />)}
      {setup.error && !data && (
        <div className="tree-error">
          Couldn’t load configuration.{" "}
          <button onClick={setup.refresh}>Retry</button>
        </div>
      )}
      {setup.loading && !data && <div className="tree-loading">Loading…</div>}
      {server.length > 0 && (
        <div role="group" aria-label="Server">
          <div className="tree-group">
            <span className="chevron" />
            <ShieldIcon />
            <span>Server</span>
          </div>
          {server.map((file) =>
            row(file.path, fileName(file.path), <FileIcon />, 1),
          )}
        </div>
      )}
      {data?.docsets.map((docset) => {
        const files = data.config_files.filter(
          (file) => file.kind === "folder" && file.docset === docset.name,
        );
        return (
          <div
            role="group"
            aria-label={`Docset ${docset.name}`}
            key={docset.name}
          >
            <div className="tree-group">
              <span className="chevron" />
              <FolderIcon />
              <span>{docset.name}</span>
            </div>
            {files.length ? (
              files.map((file) =>
                row(file.path, scopeLabel(file, docset), <FileIcon />, 1),
              )
            ) : (
              <div className="tree-loading" style={{ paddingLeft: 26 }}>
                No folder rules
              </div>
            )}
          </div>
        );
      })}
      {data?.config_files.some(
        (file) => file.kind === "folder" && !file.docset,
      ) && (
        <div role="group" aria-label="Other folders">
          <div className="tree-group">
            <span className="chevron" />
            <FolderIcon />
            <span>Other folders</span>
          </div>
          {data.config_files
            .filter((file) => file.kind === "folder" && !file.docset)
            .map((file) => row(file.path, file.scope, <FileIcon />, 1))}
        </div>
      )}
    </aside>
  );
}

function Flag({ on, children }: { on: boolean; children: ReactNode }) {
  return (
    <span className={`flag ${on ? "on" : "off"}`}>
      {children}: {on ? "on" : "off"}
    </span>
  );
}
function Rules({ rules }: { rules: Record<string, RuleSpec> | undefined }) {
  const entries = rulesSummary(rules);
  if (!entries.length) return <p>No rules declared.</p>;
  return (
    <ul className="rule-list">
      {entries.map(([name, rule]) => (
        <li key={name}>
          <code>{name}</code> → <code>{rule.use}</code>
          <small>
            {rule.match.join(", ")}
            {rule.exclude?.length
              ? ` (excluding ${rule.exclude.join(", ")})`
              : ""}
            {rule.enforce === false ? " · warn only" : ""}
            {rule.default ? " · default (children may replace)" : ""}
          </small>
          {rule.with && Object.keys(rule.with).length > 0 && (
            <pre>{JSON.stringify(rule.with)}</pre>
          )}
        </li>
      ))}
    </ul>
  );
}

function DocsetCard({
  docset,
  files,
  writeLock,
  onSelect,
}: {
  docset: SetupDocset;
  files: ConfigFile[];
  /** The server's global write lock (`--readonly`), which overrides grants. */
  writeLock: boolean;
  onSelect: (path: string) => void;
}) {
  return (
    <section className="card docset" aria-labelledby={`docset-${docset.name}`}>
      <h2 id={`docset-${docset.name}`}>
        {docset.name}
        {docset.home && <small>home</small>}
        {docset.readonly && <small>read only</small>}
      </h2>
      <dl className="setup-facts">
        <div>
          <dt>Paths</dt>
          <dd>
            {docset.roots.map((root) => (
              <code key={root}>{root}</code>
            ))}
            {docset.aliases.map((alias) => (
              <code key={alias} className="alias" title="Alias">
                {alias} (alias)
              </code>
            ))}
          </dd>
        </div>
        <div>
          <dt>Your access</dt>
          <dd>
            {docset.grants.length ? docset.grants.join(", ") : "none"}
            {docset.writable
              ? " · writable"
              : writeLock && docset.grants.some((grant) => grant !== "ro")
                ? " · server write lock is on"
                : " · read only for you"}
          </dd>
        </div>
        {docset.inbox && (
          <div>
            <dt>Inbox</dt>
            <dd>
              <code>{docset.inbox}</code>
            </dd>
          </div>
        )}
      </dl>
      <div className="flags">
        <Flag on={docset.okf}>Open Knowledge Format</Flag>
        <Flag on={docset.agent_skills}>Agent skills</Flag>
      </div>
      <h3>Folder rules</h3>
      {files.length ? (
        <ul className="config-list">
          {files.map((file) => (
            <li key={file.path}>
              <button onClick={() => onSelect(file.path)}>
                <FileIcon />
                <span>
                  <strong>{scopeLabel(file, docset)}</strong>
                  <small>{file.path}</small>
                </span>
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <p>
          No <code>.lore/config.yaml</code> under this docset.
        </p>
      )}
      {docset.roles && (
        <>
          <h3>Roles</h3>
          <div className="policy">
            {docset.roles.length ? (
              docset.roles.map((role) => (
                <span className={role.denied ? "denied" : ""} key={role.role}>
                  {role.role}: {role.denied ? "denied" : role.grant}
                </span>
              ))
            ) : (
              <p>No role grants; only home and delegated access apply.</p>
            )}
          </div>
          {docset.config_edit && docset.config_edit.length > 0 && (
            <p>
              Folder rules may be edited by: {docset.config_edit.join(", ")}
            </p>
          )}
          <h3>Rules from lore.json</h3>
          <Rules rules={docset.rules} />
        </>
      )}
    </section>
  );
}

function Overview({
  setup,
  onSelect,
  onCopy,
}: {
  setup: Setup;
  onSelect: (path: string) => void;
  onCopy: (value: string) => void;
}) {
  const server = serverFiles(setup.config_files);
  const serverConfig = server.find((file) => file.kind === "server");
  const connection = [
    { label: "SSH", value: setup.server.ssh, hint: "Agents connect here" },
    { label: "Dashboard", value: `${setup.server.http_url}/dashboard/` },
    ...(setup.server.mcp_url
      ? [{ label: "MCP", value: setup.server.mcp_url, hint: "Streamable HTTP" }]
      : []),
    {
      label: "Published files",
      value: `${setup.server.http_url}${setup.server.lore_path}/<path>`,
    },
  ];
  return (
    <div className="setup-grid">
      <section className="card" aria-labelledby="setup-session">
        <h2 id="setup-session">Your session</h2>
        <dl className="setup-facts">
          <div>
            <dt>Identity</dt>
            <dd>{setup.identity}</dd>
          </div>
          <div>
            <dt>Roles</dt>
            <dd>{setup.roles.length ? setup.roles.join(", ") : "none"}</dd>
          </div>
          {setup.home && (
            <div>
              <dt>Home docset</dt>
              <dd>{setup.home}</dd>
            </div>
          )}
        </dl>
        <p>
          Roles come from <code>lore.json</code>. Each docset below lists the
          grants they give you.
        </p>
      </section>
      <section className="card" aria-labelledby="setup-connect">
        <h2 id="setup-connect">Connect</h2>
        <ul className="connection-list">
          {connection.map((item) => (
            <li key={item.label}>
              <span>
                <strong>{item.label}</strong>
                {item.hint && <small>{item.hint}</small>}
              </span>
              <code>{item.value}</code>
              <button
                className="icon-button"
                aria-label={`Copy ${item.label}`}
                title={`Copy ${item.label}`}
                onClick={() => onCopy(item.value)}
              >
                <CopyIcon />
              </button>
            </li>
          ))}
        </ul>
        <div className="flags">
          <Flag on={!setup.server.readonly}>Writes</Flag>
          <Flag on={setup.server.analytics}>Analytics</Flag>
          <Flag on={setup.server.history}>History</Flag>
          <Flag on={setup.server.skills}>Skills plugin</Flag>
        </div>
        <p>
          These come from{" "}
          {serverConfig ? (
            <button
              className="link-button"
              onClick={() => onSelect(serverConfig.path)}
            >
              openlore.yml
            </button>
          ) : (
            <code>openlore.yml</code>
          )}{" "}
          on the host; the server reads it at startup.
        </p>
      </section>
      <section className="card" aria-labelledby="setup-auth">
        <h2 id="setup-auth">Server configuration</h2>
        {server.length ? (
          <ul className="config-list">
            {server.map((file) => (
              <li key={file.path}>
                <button onClick={() => onSelect(file.path)}>
                  <ShieldIcon />
                  <span>
                    <strong>{fileName(file.path)}</strong>
                    <small>{serverFileSummary[file.kind]}</small>
                  </span>
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <p>
            <code>lore.json</code> defines docsets, roles and identities;{" "}
            <code>openlore.yml</code> holds deployment settings. Your session
            cannot read them; that needs the <code>lore:config:edit</code>{" "}
            capability and a full-scope session.
          </p>
        )}
        {setup.rules && (
          <>
            <h3>Rules for every docset</h3>
            <Rules rules={setup.rules} />
          </>
        )}
        <p>
          Read more: <a href={`${docsURL}/docsets/`}>docsets</a>,{" "}
          <a href={`${docsURL}/auth/`}>auth</a>,{" "}
          <a href={`${docsURL}/folder-rules/`}>folder rules</a>,{" "}
          <a href={`${docsURL}/configure-the-server/`}>configure the server</a>.
        </p>
      </section>
      {setup.docsets.map((docset) => (
        <DocsetCard
          key={docset.name}
          docset={docset}
          writeLock={setup.server.readonly}
          files={setup.config_files.filter(
            (file) => file.kind === "folder" && file.docset === docset.name,
          )}
          onSelect={onSelect}
        />
      ))}
      {!setup.docsets.length && (
        <section className="card">
          <h2>No docsets</h2>
          <p>Your roles grant access to no docset.</p>
        </section>
      )}
      {(setup.truncated || setup.notes.length > 0) && (
        <section className="card setup-notes" aria-label="Notes">
          {setup.truncated && (
            <p role="alert">
              Only the first 10,000 folders were scanned; the list of folder
              rules may be incomplete.
            </p>
          )}
          {setup.notes.map((note) => (
            <p key={note}>{note}</p>
          ))}
        </section>
      )}
    </div>
  );
}

function ConfigFileView({
  file,
  docset,
  onCopy,
}: {
  file: ConfigFile;
  docset?: SetupDocset;
  onCopy: (value: string) => void;
}) {
  const state = useAsync((signal) => api.file(file.path, signal), [file.path]);
  const meta = {
    auth: {
      kind: "Access policy (lore.json)",
      applies: "every docset",
      reload: "lore config reload",
      reference: `${docsURL}/auth/`,
    },
    server: {
      kind: "Server settings (openlore.yml)",
      applies: "the whole server",
      reload: "restart the server",
      reference: `${docsURL}/openlore-yml/`,
    },
    folder: {
      kind: "Folder rules (.lore/config.yaml)",
      applies: `${file.scope} and below`,
      reload: "applies on save",
      reference: `${docsURL}/folder-rules/`,
    },
  }[file.kind];
  return (
    <div className="config-view">
      <section className="card config-meta">
        <dl className="setup-facts">
          <div>
            <dt>Kind</dt>
            <dd>{meta.kind}</dd>
          </div>
          <div>
            <dt>Applies to</dt>
            <dd>
              <code>{meta.applies}</code>
            </dd>
          </div>
          <div>
            <dt>Changes take effect</dt>
            <dd>{meta.reload}</dd>
          </div>
          {docset && (
            <div>
              <dt>Docset</dt>
              <dd>
                {docset.name} · {scopeLabel(file, docset)}
              </dd>
            </div>
          )}
          {docset?.config_edit && (
            <div>
              <dt>Editable by</dt>
              <dd>{docset.config_edit.join(", ") || "no role"}</dd>
            </div>
          )}
        </dl>
        <div className="link-actions">
          <button onClick={() => onCopy(file.path)}>Copy agent path</button>
          <a
            href={
              file.kind === "auth"
                ? `${docsURL}/auth/`
                : `${docsURL}/folder-rules/`
            }
          >
            Format reference
          </a>
        </div>
      </section>
      <section className="card config-source" aria-label="File source">
        {state.loading ? (
          <div className="state loading" role="status">
            Loading configuration…
          </div>
        ) : state.error ? (
          <div className="state error" role="alert">
            <strong>Couldn’t open this file</strong>
            <p>{state.error.message}</p>
          </div>
        ) : (
          <pre className="source">
            <code>{state.data?.source ?? ""}</code>
          </pre>
        )}
      </section>
    </div>
  );
}

/** The Setup view's main pane: the overview, or one selected config file. */
export function SetupView({
  setup,
  path,
  onSelect,
  onCopy,
}: {
  setup: SetupState;
  path: string;
  onSelect: (path: string) => void;
  onCopy: (value: string) => void;
}) {
  const data = setup.data;
  const file = data?.config_files.find((item) => item.path === path);
  const docset =
    file && data?.docsets.find((item) => item.name === file.docset);
  return (
    <>
      <div className="workspace-head">
        <div>
          <div className="breadcrumbs">
            <button onClick={() => onSelect("/")}>Setup</button>
            {file && (
              <span>
                / <button onClick={() => onSelect(path)}>{file.path}</button>
                <button
                  className="icon-button"
                  aria-label="Copy path"
                  title="Copy agent path"
                  onClick={() => onCopy(file.path)}
                >
                  <CopyIcon />
                </button>
              </span>
            )}
          </div>
          <h1>{file ? fileName(file.path) : "How this workspace is set up"}</h1>
        </div>
      </div>
      {setup.error && !data ? (
        <div className="state error" role="alert">
          <strong>Setup unavailable</strong>
          <p>{setup.error.message}</p>
          <button onClick={setup.refresh}>Retry</button>
        </div>
      ) : !data ? (
        <div className="state loading" role="status">
          Loading configuration…
        </div>
      ) : path !== "/" && !file ? (
        <div className="state error" role="alert">
          This configuration file is unavailable or no longer readable.
          <button onClick={() => onSelect("/")}>Back to overview</button>
        </div>
      ) : file ? (
        <ConfigFileView
          key={file.path}
          file={file}
          docset={docset}
          onCopy={onCopy}
        />
      ) : (
        <Overview setup={data} onSelect={onSelect} onCopy={onCopy} />
      )}
    </>
  );
}
