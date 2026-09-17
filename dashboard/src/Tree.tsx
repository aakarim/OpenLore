import { api } from "./api";
import { useAsync } from "./hooks";
import { FileIcon, FolderIcon } from "./icons";

type Props = {
  selected: string;
  expanded: string[];
  onExpanded: (paths: string[]) => void;
  onFile: (path: string) => void;
  onFolder?: (path: string) => void;
};
function Branch({
  path,
  depth,
  props,
}: {
  path: string;
  depth: number;
  props: Props;
}) {
  const open = props.expanded.includes(path);
  const state = useAsync((signal) => api.tree(path, signal), [path], open);
  const entries = state.data?.entries;
  const error = state.error;
  if (!open) return null;
  return (
    <div role="group">
      {error && (
        <div className="tree-error">
          Couldn’t load folder. <button onClick={state.refresh}>Retry</button>
        </div>
      )}
      {!entries && !error && (
        <div className="tree-loading" style={{ paddingLeft: 14 + depth * 16 }}>
          Loading…
        </div>
      )}
      {entries?.map((entry) => (
        <div key={entry.path}>
          <button
            className={`tree-row ${props.selected === entry.path ? "selected" : ""}`}
            style={{ paddingLeft: 10 + depth * 16 }}
            onClick={() => {
              if (entry.directory) {
                props.onFolder?.(entry.path);
                togglePath(entry.path, props);
              } else props.onFile(entry.path);
            }}
            aria-current={props.selected === entry.path ? "page" : undefined}
            aria-expanded={
              entry.directory ? props.expanded.includes(entry.path) : undefined
            }
          >
            {entry.directory && (
              <span className="chevron" aria-hidden>
                {props.expanded.includes(entry.path) ? "⌄" : "›"}
              </span>
            )}
            {!entry.directory && <span className="chevron" />}
            {entry.directory ? <FolderIcon /> : <FileIcon />}
            <span>{entry.name}</span>
          </button>
          {entry.directory && (
            <Branch path={entry.path} depth={depth + 1} props={props} />
          )}
        </div>
      ))}
    </div>
  );
}
function togglePath(path: string, props: Props) {
  props.onExpanded(
    props.expanded.includes(path)
      ? props.expanded.filter((item) => item !== path)
      : [...props.expanded, path],
  );
}
export function Tree(props: Props) {
  return (
    <aside className="sidebar" aria-label="Knowledge tree">
      <div className="sidebar-title">
        <span>KNOWLEDGE</span>
      </div>
      <button
        className={`tree-row root ${props.selected === "/" ? "selected" : ""}`}
        aria-expanded={props.expanded.includes("/")}
        onClick={() => {
          props.onFolder?.("/");
          togglePath("/", props);
        }}
      >
        <span className="chevron">
          {props.expanded.includes("/") ? "⌄" : "›"}
        </span>
        <FolderIcon />
        <span>Workspace</span>
      </button>
      <Branch path="/" depth={1} props={props} />
    </aside>
  );
}
