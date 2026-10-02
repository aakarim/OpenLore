import { useEffect, useState } from "react";
import { api } from "./api";
import { useAsync } from "./hooks";
import { Sunburst, estimatedTokens } from "./Sunburst";
import type {
  Access,
  AnalyticsStatus,
  AnalyticsTab,
  ContextNode,
  Materialized,
  Usage,
} from "./types";

const tabs: { id: AnalyticsTab; label: string; glyph: string }[] = [
  { id: "overview", label: "Overview", glyph: "▦" },
  { id: "knowledge", label: "Knowledge", glyph: "◫" },
  { id: "usage", label: "Usage", glyph: "⌁" },
  { id: "gaps", label: "Gaps", glyph: "?" },
  { id: "commands", label: "Commands", glyph: "⌘" },
  { id: "access", label: "Access", glyph: "◇" },
];
const n = (value: number | undefined) => (value || 0).toLocaleString("en-GB");
// Processing boundaries are UTC segment boundaries, so format them in UTC.
export const formatDate = (value: string) =>
  new Date(value).toLocaleDateString("en-GB", {
    day: "numeric",
    month: "short",
    year: "numeric",
    timeZone: "UTC",
  });
function AnalyticsProgress({
  label,
  status,
  loading,
  error,
}: {
  label: string;
  status?: AnalyticsStatus;
  loading: boolean;
  error?: Error;
}) {
  const busy = status?.updating || loading;
  const state = status?.state || (error ? "failed" : "ready");
  const ready = !busy && state === "ready";
  const progress = status?.progress;
  const detail = busy
    ? progress?.phase === "history"
      ? "Processing historical activity"
      : progress?.phase === "content"
        ? "Scanning workspace content"
        : "Preparing analytics"
    : ready
      ? "Up to date"
      : state === "failed"
        ? "Processing stopped"
        : status?.state === "disabled"
          ? "Processing paused"
          : "Not available yet";
  return (
    <div className="analytics-progress" data-state={busy ? "updating" : state}>
      <div className="analytics-progress-heading" role="status">
        <strong>{label}</strong>
        <span>
          {detail}
          {busy && progress
            ? progress.phase === "history" && progress.since
              ? ` · processed back to ${formatDate(progress.since)}`
              : ` · ${n(progress.processed)} ${progress.unit} processed${progress.phase === "history" ? " this session" : ""}`
            : ""}
        </span>
      </div>
      <div
        className="analytics-progress-track"
        role="progressbar"
        aria-label={`${label} progress`}
        aria-valuetext={detail}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={busy ? undefined : ready ? 100 : 0}
      >
        <span />
      </div>
      {status?.coverage && !busy && (!ready || !status.complete) && (
        <p className="coverage-note">{status.coverage}</p>
      )}
      {status?.warning && <p className="coverage-note">{status.warning}</p>}
      {status?.error && (
        <p className="coverage-note" role="alert">
          {status.error}
        </p>
      )}
    </div>
  );
}
function State({
  loading,
  error,
  children,
}: {
  loading: boolean;
  error?: Error;
  children: React.ReactNode;
}) {
  if (error)
    return (
      <div className="state error" role="alert">
        <strong>Analytics unavailable</strong>
        <p>{error.message}</p>
      </div>
    );
  if (loading)
    return (
      <div className="state loading" role="status">
        Computing analytics…
      </div>
    );
  return <>{children}</>;
}
function ScopeFacts({
  context,
  ratio,
  window,
}: {
  context: ContextNode;
  ratio: 4 | 6;
  window: number;
}) {
  const tokens = estimatedTokens(context, ratio);
  return (
    <dl className="metrics">
      <div>
        <dt>Estimated context</dt>
        <dd>{n(tokens)} tokens</dd>
      </div>
      <div>
        <dt>Files / size</dt>
        <dd>
          {countFiles(context)} · {formatBytes(context.bytes)}
        </dd>
      </div>
      <div>
        <dt>Lines</dt>
        <dd>{n(context.lines)}</dd>
      </div>
      <div>
        <dt>Context window used</dt>
        <dd>{((tokens / window) * 100).toFixed(2)}%</dd>
      </div>
    </dl>
  );
}
function countFiles(node: ContextNode): number {
  if (!node.directory) return 1;
  return node.children?.reduce((sum, child) => sum + countFiles(child), 0) || 0;
}
function formatBytes(bytes: number) {
  return bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} KB`;
}
function Activity({ usage }: { usage: Usage }) {
  const activity = usage.activity ?? [];
  const max = Math.max(
      1,
      ...activity.map((day) => day.human + day.agent + day.unknown),
    ),
    width = 440,
    height = 135,
    step = width / Math.max(1, activity.length);
  return (
    <section className="card activity-card">
      <span className="eyebrow">ACTIVITY · SELECTED RANGE</span>
      <h2>Activity</h2>
      <div className="activity-summary">
        <span>
          Human writes<strong>{n(usage.human_writes)}</strong>
        </span>
        <span>
          Agent writes<strong>{n(usage.agent_writes)}</strong>
        </span>
        <span>
          Unknown<strong>{n(usage.unknown_writes)}</strong>
        </span>
        <span>
          Reads<strong>{n(usage.reads)}</strong>
        </span>
      </div>
      {activity.length ? (
        <svg
          className="activity-chart"
          viewBox={`0 0 ${width} ${height + 24}`}
          aria-label="Daily activity stacked by attribution"
          role="img"
        >
          {activity.map((day, index) => {
            const x = index * step + 2,
              values = [day.human, day.agent, day.unknown],
              colors = ["#71b6b0", "#9e8cd0", "#87909e"];
            let y = height;
            return (
              <g key={day.date}>
                {values.map((value, i) => {
                  const h = (value / max) * height;
                  y -= h;
                  return (
                    <rect
                      key={i}
                      x={x}
                      y={y}
                      width={Math.max(2, step - 4)}
                      height={h}
                      fill={colors[i]}
                    >
                      <title>
                        {day.date}: {value}
                      </title>
                    </rect>
                  );
                })}
              </g>
            );
          })}
          <text x="0" y={height + 20}>
            {activity[0]?.date}
          </text>
          <text x={width} y={height + 20} textAnchor="end">
            Today
          </text>
        </svg>
      ) : (
        <p className="empty">No activity in this time range.</p>
      )}
      <div className="legend">
        <span className="human">Human</span>
        <span className="agent">Agent</span>
        <span className="unknown">Unknown</span>
      </div>
    </section>
  );
}
function ActivityPending({ usage, error }: { usage?: Usage; error?: Error }) {
  const progress = usage?.analytics?.progress;
  return (
    <section className="card activity-card">
      <span className="eyebrow">ACTIVITY · SELECTED RANGE</span>
      <h2>Activity</h2>
      {error ? (
        <div className="state error" role="alert">
          <strong>Activity unavailable</strong>
          <p>{error.message}</p>
        </div>
      ) : (
        <p className="empty" role="status">
          Activity totals will appear when this time range has been processed.
          {progress?.since && ` Processed back to ${formatDate(progress.since)}.`}
        </p>
      )}
    </section>
  );
}
function completeUsage(usage?: Usage) {
  return usage && (!usage.analytics || usage.analytics.complete)
    ? usage
    : undefined;
}
function Overview({
  context,
  usage,
  usageError,
  ratio,
  window,
  onScope,
  onTab,
  access,
  days,
}: {
  context: ContextNode;
  usage?: Usage;
  usageError?: Error;
  ratio: 4 | 6;
  window: number;
  onScope: (path: string) => void;
  onTab: (tab: AnalyticsTab) => void;
  access: boolean;
  days: number;
}) {
  const gaps = useAsync(
    (signal) =>
      api.aggregation("top-unfilled-queries", context.path, days, signal),
    [context.path, days],
  );
  const policy = useAsync(
    (signal) => api.access(context.path, signal),
    [context.path],
    access,
  );
  const roles = new Set(
    policy.data?.docsets.flatMap((d) =>
      d.roles.filter((r) => r.grant && !r.denied).map((r) => r.role),
    ) || [],
  );
  const ready = completeUsage(usage);
  const pending = usageError ? "Unavailable" : "Processing";
  const summaries: [AnalyticsTab, string, string, string, string][] = [
    [
      "knowledge",
      "Knowledge",
      `${n(estimatedTokens(context, ratio))} tokens`,
      `${countFiles(context)} files · ${formatBytes(context.bytes)}`,
      "CURRENT STATE",
    ],
    [
      "usage",
      "Usage",
      ready ? `${n(ready.reads)} reads` : pending,
      ready
        ? `${n(ready.estimated_tokens)} estimated input tokens`
        : "Activity in the selected range",
      "SELECTED RANGE",
    ],
    [
      "gaps",
      "Gaps",
      gaps.data?.status === "ok"
        ? `${n(gaps.data.table.total)} queries`
        : "Unavailable",
      "Distinct unfilled search terms",
      "SELECTED RANGE",
    ],
    [
      "commands",
      "Commands",
      ready ? `${n(ready.commands)} commands` : pending,
      "Command and principal activity",
      "SELECTED RANGE",
    ],
  ];
  if (access)
    summaries.splice(1, 0, [
      "access",
      "Access",
      policy.data ? `${roles.size} roles` : "Unavailable",
      "Configured grants, not user counts",
      "CURRENT STATE",
    ]);
  return (
    <>
      <div className="overview-grid">
        <section className="card context-card">
          <span className="eyebrow">CURRENT STATE · NOT TIME SCOPED</span>
          <h2>{context.directory ? "Context by folder" : "File context"}</h2>
          <Sunburst
            node={context}
            ratio={ratio}
            contextWindow={window}
            onSelect={(node) => onScope(node.path)}
          />
          <p>
            Estimate ≈ characters ÷ {ratio}. Folder totals include all
            descendants.
          </p>
        </section>
        {ready ? (
          <Activity usage={ready} />
        ) : (
          <ActivityPending usage={usage} error={usageError} />
        )}
      </div>
      <div className="summary-grid">
        {summaries.map(([tab, label, value, detail, scope]) => (
          <button className="summary-card" onClick={() => onTab(tab)} key={tab}>
            <span className="eyebrow">{scope}</span>
            <span>
              {label}
              <b>↗</b>
            </span>
            <strong>{value}</strong>
            <small>{detail}</small>
          </button>
        ))}
      </div>
    </>
  );
}
function Contribution({ usage }: { usage: Usage }) {
  const total = Math.max(
    1,
    usage.human_writes + usage.agent_writes + usage.unknown_writes,
  );
  return (
    <section className="card">
      <span className="eyebrow">ACTIVITY · SELECTED RANGE</span>
      <h2>Knowledge contribution</h2>
      <p>{n(usage.writes)} writes by attribution.</p>
      <div className="contribution" aria-label="Write attribution share">
        <i
          className="human"
          tabIndex={0}
          aria-label={`Human: ${n(usage.human_writes)} writes`}
          title={`Human: ${n(usage.human_writes)} writes`}
          style={{ width: `${(usage.human_writes / total) * 100}%` }}
        />
        <i
          className="agent"
          tabIndex={0}
          aria-label={`Agent: ${n(usage.agent_writes)} writes`}
          title={`Agent: ${n(usage.agent_writes)} writes`}
          style={{ width: `${(usage.agent_writes / total) * 100}%` }}
        />
        <i
          className="unknown"
          tabIndex={0}
          aria-label={`Unknown: ${n(usage.unknown_writes)} writes`}
          title={`Unknown: ${n(usage.unknown_writes)} writes`}
          style={{ width: `${(usage.unknown_writes / total) * 100}%` }}
        />
      </div>
      <div className="legend">
        <span className="human">Human {n(usage.human_writes)}</span>
        <span className="agent">Agent {n(usage.agent_writes)}</span>
        <span className="unknown">Unknown {n(usage.unknown_writes)}</span>
      </div>
    </section>
  );
}
function Aggregation({
  name,
  path,
  days,
  title,
  onFile,
}: {
  name: string;
  path: string;
  days: number;
  title: string;
  onFile?: (path: string) => void;
}) {
  const [revision, setRevision] = useState(0);
  const state = useAsync(
    (signal) => api.aggregation(name, path, days, signal),
    [name, path, days, revision],
  );
  useEffect(() => {
    if (!state.data?.analytics?.updating) return;
    const timer = window.setTimeout(
      () => setRevision((value) => value + 1),
      1000,
    );
    return () => window.clearTimeout(timer);
  }, [state.data?.analytics]);
  return (
    <section className="card table-card">
      <h2>{title}</h2>
      <State loading={state.loading} error={state.error}>
        {state.data?.analytics && state.data.analytics.state !== "ready" && (
          <p className="coverage-note" data-state={state.data.analytics.state}>
            {state.data.analytics.state}.
            {state.data.analytics.complete &&
              " Last complete result remains visible."}
            {state.data.analytics.error && ` ${state.data.analytics.error}`}
          </p>
        )}
        {state.data?.status !== "ok" ? (
          <p className="empty">
            {state.data?.note ||
              `Analytics are ${state.data?.status || "unavailable"}.`}
          </p>
        ) : !state.data.table.rows?.length ? (
          <p className="empty">No results in this time range.</p>
        ) : (
          <DataTable data={state.data} onFile={onFile} />
        )}
      </State>
    </section>
  );
}
function DataTable({
  data,
  onFile,
}: {
  data: Materialized;
  onFile?: (path: string) => void;
}) {
  return (
    <div className="data-table">
      <table>
        <thead>
          <tr>
            {data.table.columns.map((column) => (
              <th key={column}>{column.replaceAll("_", " ")}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {data.table.rows.map((row, i) => (
            <tr key={i}>
              {row.map((cell, j) => (
                <td key={j}>
                  {j === 0 &&
                  onFile &&
                  typeof cell === "string" &&
                  cell.startsWith("/") ? (
                    <button onClick={() => onFile(cell)}>{cell} ↗</button>
                  ) : (
                    formatCell(cell)
                  )}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
function formatCell(value: unknown) {
  if (value === null || value === undefined || value === "") return "—";
  if (typeof value === "number")
    return value.toLocaleString("en-GB", { maximumFractionDigits: 2 });
  return String(value);
}
function UsagePanel({
  path,
  days,
  usage,
  context,
  onFile,
}: {
  path: string;
  days: number;
  usage: Usage;
  context: ContextNode;
  onFile: (path: string) => void;
}) {
  return (
    <>
      <section className="card">
        <h2>Reads and writes over time</h2>
        <Activity usage={usage} />
        <dl className="metrics compact">
          <div>
            <dt>Reads</dt>
            <dd>{n(usage.reads)}</dd>
          </div>
          <div>
            <dt>Writes</dt>
            <dd>{n(usage.writes)}</dd>
          </div>
          <div>
            <dt>Estimated input</dt>
            <dd>{n(usage.estimated_tokens)}</dd>
          </div>
          <div>
            <dt>Unestimated reads</dt>
            <dd>{n(usage.unestimated_reads)}</dd>
          </div>
        </dl>
      </section>
      {context.directory && (
        <div className="two-column">
          <Aggregation
            name="most-used-files"
            title="Most-used files"
            {...{ path, days, onFile }}
          />
          <Aggregation
            name="least-used-files"
            title="Least-used files"
            {...{ path, days, onFile }}
          />
        </div>
      )}
      {!context.directory && (
        <div className="two-column">
          <Aggregation
            name="most-used-lines"
            title="Most-used lines"
            {...{ path, days, onFile }}
          />
          <Aggregation
            name="least-used-lines"
            title="Least-used lines"
            {...{ path, days, onFile }}
          />
        </div>
      )}
    </>
  );
}
function AccessPanel({ path }: { path: string }) {
  const state = useAsync((signal) => api.access(path, signal), [path]);
  return (
    <State loading={state.loading} error={state.error}>
      {state.data && <AccessContent access={state.data} />}
    </State>
  );
}
function AccessContent({ access }: { access: Access }) {
  return (
    <>
      <section className="card">
        <h2>Access</h2>
        {access.docsets.map((docset) => (
          <div className="policy" key={docset.name}>
            <h3>
              {docset.name} {docset.readonly && <small>read only</small>}
            </h3>
            <p>{docset.roots.join(" · ")}</p>
            {docset.roles.map((role) => (
              <span className={role.denied ? "denied" : ""} key={role.role}>
                {role.role}: {role.grant}
              </span>
            ))}
          </div>
        ))}
      </section>
      <section className="card">
        <h2>Folder rules</h2>
        {access.folder_rules.map((rule, index) => (
          <details key={index}>
            <summary>{rule.scope}</summary>
            <p>{rule.origin}</p>
            <pre>{JSON.stringify(rule.rules, null, 2)}</pre>
          </details>
        ))}
        {access.notes.map((note) => (
          <p key={note}>{note}</p>
        ))}
      </section>
    </>
  );
}

export function Analytics({
  path,
  tab,
  days,
  ratio,
  contextWindow,
  canAccess,
  onTab,
  onScope,
  onFile,
  onComputed,
  onDays,
}: {
  path: string;
  tab: AnalyticsTab;
  days: number;
  ratio: 4 | 6;
  contextWindow: number;
  canAccess: boolean;
  onTab: (tab: AnalyticsTab) => void;
  onScope: (path: string) => void;
  onFile: (path: string) => void;
  onComputed: (time: string) => void;
  onDays: (days: number) => void;
}) {
  const [visitedTabs, setVisitedTabs] = useState<Set<AnalyticsTab>>(
    () => new Set([tab]),
  );
  const [analyticsRevision, setAnalyticsRevision] = useState(0);
  useEffect(() => {
    setVisitedTabs((visited) => {
      if (visited.has(tab)) return visited;
      return new Set([...visited, tab]);
    });
  }, [tab]);
  const tabHasBeenVisited = (candidate: AnalyticsTab) =>
    candidate === tab || visitedTabs.has(candidate);
  const needsUsage = ["overview", "knowledge", "usage"].some((candidate) =>
    tabHasBeenVisited(candidate as AnalyticsTab),
  );
  const context = useAsync(
    (signal) => api.context(path, signal),
    [path, analyticsRevision],
    true,
    true,
  );
  const usage = useAsync(
    (signal) => api.usage(path, days, ratio, signal),
    [path, days, ratio, analyticsRevision],
    needsUsage,
    true,
  );
  useEffect(() => {
    if (context.loading || usage.loading) return;
    const statuses = [context.data?.analytics, usage.data?.analytics];
    if (
      !statuses.some((status) => status?.updating || status?.state === "cold")
    )
      return;
    const timer = window.setTimeout(
      () => setAnalyticsRevision((value) => value + 1),
      1000,
    );
    return () => window.clearTimeout(timer);
  }, [
    context.data?.analytics,
    usage.data?.analytics,
    context.loading,
    usage.loading,
  ]);
  useEffect(() => {
    if (usage.data?.computed_at) onComputed(usage.data.computed_at);
  }, [usage.data?.computed_at, onComputed]);
  const visibleTabs = tabs.filter((item) => item.id !== "access" || canAccess);
  const readyUsage = completeUsage(usage.data);
  const activity = (render: (data: Usage) => React.ReactNode) =>
    usage.error && !usage.data ? (
      <div className="state error" role="alert">
        <strong>Activity unavailable</strong>
        <p>{usage.error.message}</p>
      </div>
    ) : readyUsage ? (
      render(readyUsage)
    ) : usage.loading && !usage.data ? (
      <div className="state loading" role="status">
        Computing activity…
      </div>
    ) : (
      <ActivityPending usage={usage.data} />
    );
  const body = (selectedTab: AnalyticsTab) => {
    switch (selectedTab) {
      case "overview":
        return (
          context.data && (
            <Overview
              context={context.data}
              usage={usage.data}
              usageError={usage.data ? undefined : usage.error}
              ratio={ratio}
              window={contextWindow}
              onScope={onScope}
              onTab={onTab}
              access={canAccess}
              days={days}
            />
          )
        );
      case "knowledge":
        return (
          context.data && (
            <>
              <section className="card">
                <span className="eyebrow">CURRENT STATE · NOT TIME SCOPED</span>
                <h2>Knowledge & context</h2>
                <ScopeFacts
                  context={context.data}
                  ratio={ratio}
                  window={contextWindow}
                />
              </section>
              {activity((data) => (
                <Contribution usage={data} />
              ))}
            </>
          )
        );
      case "usage":
        return (
          context.data &&
          activity((data) => (
            <UsagePanel
              path={path}
              days={days}
              usage={data}
              context={context.data!}
              onFile={onFile}
            />
          ))
        );
      case "gaps":
        return (
          <>
            <Aggregation
              name="top-unfilled-queries"
              title="Unfilled search queries"
              {...{ path, days }}
            />
            <Aggregation
              name="top-search-queries"
              title="Top search queries"
              {...{ path, days }}
            />
          </>
        );
      case "commands":
        return (
          <>
            <Aggregation
              name="top-commands"
              title="Top commands"
              {...{ path, days }}
            />
            <Aggregation
              name="commands-by-principal"
              title="Commands by principal"
              {...{ path, days }}
            />
            <Aggregation
              name="unknown-commands"
              title="Unknown commands"
              {...{ path, days }}
            />
          </>
        );
      case "access":
        return canAccess ? <AccessPanel path={path} /> : null;
    }
  };
  return (
    <>
      {context.data && !context.data.directory && (
        <div className="scope-explorer">
          <div className="file-scope">
            <span>Single-file analytics</span>
            <button className="primary" onClick={() => onFile(path)}>
              View ↗
            </button>
          </div>
        </div>
      )}
      <div
        className="analytics-tabs"
        role="tablist"
        aria-label="Analytics sections"
      >
        {visibleTabs.map((item) => (
          <button
            role="tab"
            aria-selected={tab === item.id}
            tabIndex={tab === item.id ? 0 : -1}
            key={item.id}
            onClick={() => onTab(item.id)}
            onKeyDown={(event) => {
              const index = visibleTabs.findIndex((t) => t.id === tab);
              const next =
                event.key === "ArrowRight"
                  ? (index + 1) % visibleTabs.length
                  : event.key === "ArrowLeft"
                    ? (index + visibleTabs.length - 1) % visibleTabs.length
                    : event.key === "Home"
                      ? 0
                      : event.key === "End"
                        ? visibleTabs.length - 1
                        : -1;
              if (next >= 0) {
                event.preventDefault();
                onTab(visibleTabs[next].id);
                (
                  event.currentTarget.parentElement?.children[
                    next
                  ] as HTMLElement
                )?.focus();
              }
            }}
          >
            {item.label}
          </button>
        ))}
      </div>
      {tab !== "access" && (
        <div className="range">
          <label>
            Time range:{" "}
            <select
              value={days}
              onChange={(e) => onDays(Number(e.target.value))}
            >
              <option value="1">Last 24 hours</option>
              <option value="7">Last 7 days</option>
              <option value="30">Last 30 days</option>
              <option value="90">Last 90 days</option>
            </select>
          </label>
          <small>
            Applies to activity only, not current content or access.
          </small>
        </div>
      )}
      <section
        className="analytics-processing"
        aria-label="Background analytics"
      >
        <AnalyticsProgress
          label="Knowledge analytics"
          status={context.data?.analytics}
          loading={context.loading && !context.data}
          error={!context.data ? context.error : undefined}
        />
        {needsUsage && (
          <AnalyticsProgress
            label="Activity analytics"
            status={usage.data?.analytics}
            loading={usage.loading && !usage.data}
            error={!usage.data ? usage.error : undefined}
          />
        )}
      </section>
      {((context.error && context.data) || (usage.error && usage.data)) && (
        <p role="alert" className="coverage-note">
          Could not refresh analytics. Previous results remain visible.
        </p>
      )}
      <State
        loading={
          ["overview", "knowledge", "usage"].includes(tab) &&
          context.loading &&
          !context.data
        }
        error={
          ["overview", "knowledge", "usage"].includes(tab) && !context.data
            ? context.error
            : undefined
        }
      >
        <div className="analytics-content">
          {visibleTabs
            .filter((item) => tabHasBeenVisited(item.id))
            .map((item) => (
              <div key={item.id} hidden={item.id !== tab}>
                {body(item.id)}
              </div>
            ))}
        </div>
        {usage.data?.note && <p className="coverage-note">{usage.data.note}</p>}
        {tab !== "access" && (
          <p className="coverage-note">
            No recorded use means no retained matching event in this range, not
            necessarily never used. Browser previews are not counted.
          </p>
        )}
      </State>
    </>
  );
}
export { tabs as analyticsTabs };
