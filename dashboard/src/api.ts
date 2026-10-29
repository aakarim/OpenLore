import type {
  Access,
  AnalyticsProcessingStatus,
  ContextNode,
  FileResponse,
  FilesResponse,
  HistoryResponse,
  Materialized,
  Session,
  Setup,
  Source,
  SourceSnapshot,
  TreeResponse,
  Usage,
} from "./types";

export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
    public loginURL?: string,
    /** Operator actions, in order, the server says unblock the dashboard. */
    public nextSteps?: string[],
  ) {
    super(message);
  }
}

async function request<T>(
  path: string,
  signal?: AbortSignal,
  text = false,
): Promise<T> {
  const response = await fetch(`/dashboard/api/${path}`, {
    credentials: "same-origin",
    signal,
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    let body: { error?: string; login_url?: string; next_steps?: string[] } =
      {};
    try {
      body = await response.json();
    } catch {
      /* use status */
    }
    if (response.status === 401 && path !== "session" && !signal?.aborted)
      dispatchEvent(new Event("dashboard-auth-expired"));
    throw new APIError(
      body.error || `Request failed (${response.status})`,
      response.status,
      body.login_url,
      body.next_steps,
    );
  }
  return (text ? response.text() : response.json()) as Promise<T>;
}
async function absoluteRequest<T>(
  path: string,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    signal,
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    if (response.status === 401 && !signal?.aborted)
      dispatchEvent(new Event("dashboard-auth-expired"));
    throw new APIError(`Request failed (${response.status})`, response.status);
  }
  return response.json() as Promise<T>;
}
const query = (params: Record<string, string | number>) =>
  new URLSearchParams(
    Object.entries(params).map(([k, v]) => [k, String(v)]),
  ).toString();
export const api = {
  session: (signal?: AbortSignal) => request<Session>("session", signal),
  tree: (path: string, signal?: AbortSignal) =>
    request<TreeResponse>(`tree?${query({ path })}`, signal),
  files: (path: string, signal?: AbortSignal) =>
    request<FilesResponse>(`files?${query({ path })}`, signal),
  context: (path: string, signal?: AbortSignal) =>
    request<ContextNode>(`context?${query({ path })}`, signal),
  file: (path: string, signal?: AbortSignal) =>
    request<FileResponse>(`file?${query({ path })}`, signal),
  history: (path: string, cursor = "", signal?: AbortSignal) =>
    request<HistoryResponse>(`history?${query({ path, cursor })}`, signal),
  usage: (path: string, days: number, ratio: number, signal?: AbortSignal) =>
    request<Usage>(`usage?${query({ path, days, ratio })}`, signal),
  analyticsStatus: (signal?: AbortSignal) =>
    request<AnalyticsProcessingStatus>("analytics-status", signal),
  access: (path: string, signal?: AbortSignal) =>
    request<Access>(`access?${query({ path })}`, signal),
  setup: (signal?: AbortSignal) => request<Setup>("setup", signal),
  sources: (signal?: AbortSignal) => request<Source[]>("sources", signal),
  sourceHistory: (store: string, signal?: AbortSignal) =>
    request<SourceSnapshot[]>(
      `sources?${query({ store, history: "true" })}`,
      signal,
    ),
  sourceFile: (store: string, file: string, signal?: AbortSignal) =>
    request<string>(`sources?${query({ store, file })}`, signal, true),
  aggregation: (
    name: string,
    path: string,
    days: number,
    signal?: AbortSignal,
  ) =>
    absoluteRequest<Materialized>(
      `/analytics/aggregations/${name}?${query({ path, since: `${days}d`, fresh: "true", limit: 100 })}`,
      signal,
    ),
};
