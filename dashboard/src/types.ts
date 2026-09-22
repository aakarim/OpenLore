export type Session = { identity: string; lore_path: string; access: boolean };
export type TreeEntry = {
  path: string;
  name: string;
  directory: boolean;
  bytes: number;
};
export type TreeResponse = { path: string; entries: TreeEntry[] };
export type ContextNode = {
  path: string;
  name: string;
  directory: boolean;
  bytes: number;
  lines: number;
  characters: number;
  tokens: number;
  children?: ContextNode[];
  analytics?: AnalyticsStatus;
};
export type AnalyticsStatus = {
  state:
    | "ready"
    | "cold"
    | "updating"
    | "stale"
    | "disabled"
    | "failed"
    | "unavailable";
  computed_at?: string;
  updating: boolean;
  complete: boolean;
  coverage?: string;
  error?: string;
  warning?: string;
  progress?: { phase: "content" | "history"; processed: number; unit: string };
};
export type Facts = Pick<
  ContextNode,
  "bytes" | "lines" | "characters" | "tokens"
>;
export type FileResponse = {
  path: string;
  name: string;
  source?: string;
  html?: string;
  content_type: string;
  facts: Facts;
  binary: boolean;
};
export type HistoryEntry = {
  time: string;
  attribution: string;
  action: string;
  hash: string;
};
export type HistoryResponse = {
  available: boolean;
  entries: HistoryEntry[];
  next_cursor?: string;
};
export type UsagePoint = {
  date: string;
  human: number;
  agent: number;
  unknown: number;
  reads: number;
  writes: number;
};
export type Usage = {
  reads: number;
  hits: number;
  writes: number;
  commands: number;
  human_writes: number;
  agent_writes: number;
  unknown_writes: number;
  estimated_tokens: number;
  estimated_reads: number;
  unestimated_reads: number;
  activity: UsagePoint[];
  computed_at: string;
  note?: string;
  analytics?: AnalyticsStatus;
};
export type Materialized = {
  status: string;
  table: { columns: string[]; rows: unknown[][]; total: number };
  computed_at: string;
  window: unknown;
  note?: string;
  analytics?: AnalyticsStatus;
};
export type Access = {
  path: string;
  docsets: {
    name: string;
    roots: string[];
    roles: { role: string; grant: string; denied: boolean }[];
    readonly: boolean;
  }[];
  folder_rules: { origin: string; scope: string; rules: object }[];
  notes: string[];
};
export type AnalyticsTab =
  "overview" | "knowledge" | "usage" | "gaps" | "commands" | "access";
