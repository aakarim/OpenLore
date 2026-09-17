import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import { useAsync } from "./hooks";
import type { FileResponse, HistoryResponse, Session } from "./types";

const basename = (path: string) =>
  path.split("/").filter(Boolean).at(-1) || "Workspace";
const parent = (path: string) => path.slice(0, path.lastIndexOf("/")) || "/";
const decode = (value: string) => {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
};
export function resolveLorePath(relative: string, filePath: string) {
  const value = relative.split("#")[0].split("?")[0];
  const segments = (
    value.startsWith("/") ? value : `${parent(filePath)}/${value}`
  ).split("/");
  const clean: string[] = [];
  for (const segment of segments) {
    if (!segment || segment === ".") continue;
    if (segment === "..") clean.pop();
    else clean.push(decode(segment));
  }
  return `/${clean.join("/")}`;
}

const encodedLoreURL = (lorePath: string, path: string) =>
  `${lorePath.replace(/\/$/, "")}/${path
    .split("/")
    .filter(Boolean)
    .map(encodeURIComponent)
    .join("/")}`;

function internalPath(raw: string, filePath: string, session: Session) {
  if (raw.startsWith("#")) return null;
  const absolute = new URL(raw, location.href);
  if (absolute.origin !== location.origin) return null;
  const lorePrefix = session.lore_path.replace(/\/$/, "");
  if (/^(https?:)?\/\//i.test(raw) || raw.startsWith("/")) {
    if (
      absolute.pathname !== lorePrefix &&
      !absolute.pathname.startsWith(`${lorePrefix}/`)
    )
      return null;
    return resolveLorePath(absolute.pathname.slice(lorePrefix.length), "/");
  }
  return resolveLorePath(raw, filePath);
}
function Markdown({
  file,
  session,
  onFile,
}: {
  file: FileResponse;
  session: Session;
  onFile: (path: string) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const root = ref.current;
    if (!root) return;
    root.querySelectorAll<HTMLAnchorElement>("a[href]").forEach((anchor) => {
      const raw = anchor.getAttribute("href") || "";
      if (raw.startsWith("#")) return;
      if (/^(mailto:|tel:)/i.test(raw)) {
        anchor.removeAttribute("target");
        anchor.rel = "noreferrer";
        return;
      }
      const path = internalPath(raw, file.path, session);
      if (!path) {
        if (/^(https?:)?\/\//i.test(raw)) {
          anchor.removeAttribute("target");
          anchor.rel = "noreferrer";
        } else if (/^[a-z]+:/i.test(raw)) {
          anchor.removeAttribute("href");
        }
        return;
      }
      const parsed = new URL(raw, location.href);
      const suffix = `${parsed.search}${parsed.hash}`;
      anchor.href = `${encodedLoreURL(session.lore_path, path)}${suffix}`;
      anchor.onclick = (event) => {
        if (
          event.button !== 0 ||
          event.metaKey ||
          event.ctrlKey ||
          event.shiftKey ||
          event.altKey
        )
          return;
        event.preventDefault();
        onFile(path);
      };
    });
    root.querySelectorAll<HTMLImageElement>("img[src]").forEach((image) => {
      const raw = image.getAttribute("src") || "";
      if (raw.startsWith("data:")) return;
      if (/^(blob:|javascript:)/i.test(raw)) {
        image.removeAttribute("src");
        image.alt = image.alt || "Image blocked";
        return;
      }
      const path = internalPath(raw, file.path, session);
      if (!path) {
        image.removeAttribute("src");
        image.alt = image.alt || "Remote image blocked";
        return;
      }
      image.src = `/dashboard/api/raw?${new URLSearchParams({ path })}`;
    });
  }, [file, session, onFile]);
  return (
    <div ref={ref} className="markdown">
      {file.html ? (
        <div dangerouslySetInnerHTML={{ __html: file.html }} />
      ) : (
        <pre className="plaintext-preview">{file.source ?? ""}</pre>
      )}
    </div>
  );
}
function FileInfo({
  file,
  ratio,
  contextWindow,
  onCopy,
  onAnalytics,
  onSettings,
}: {
  file: FileResponse;
  ratio: number;
  contextWindow: number;
  onCopy: (value: string) => void | Promise<void>;
  onAnalytics: () => void;
  onSettings?: () => void;
}) {
  const [copyStatus, setCopyStatus] = useState("");
  const copy = async (value: string, label: string) => {
    try {
      await onCopy(value);
      setCopyStatus(`${label} copied.`);
    } catch {
      setCopyStatus(`Couldn’t copy ${label.toLowerCase()}.`);
    }
  };
  const estimatedTokens =
    ratio > 0 ? Math.ceil(file.facts.characters / ratio) : 0;
  return (
    <div className="file-info">
      <span className="eyebrow">CURRENT STATE</span>
      <dl className="metrics">
        <div>
          <dt>Context size · estimated tokens</dt>
          <dd>{estimatedTokens.toLocaleString()}</dd>
        </div>
        <div>
          <dt>File size</dt>
          <dd>{(file.facts.bytes / 1024).toFixed(1)} KB</dd>
        </div>
        <div>
          <dt>Lines</dt>
          <dd>{file.facts.lines.toLocaleString()}</dd>
        </div>
        <div>
          <dt>Context window used</dt>
          <dd>
            {contextWindow > 0
              ? `${((estimatedTokens / contextWindow) * 100).toFixed(2)}%`
              : "—"}
          </dd>
        </div>
      </dl>
      <p>Activity over time is in Analytics → Usage.</p>
      <code className="path-code">{file.path}</code>
      <div className="link-actions">
        <button onClick={() => void copy(file.path, "Agent path")}>
          Copy agent path
        </button>
        <button onClick={() => void copy(location.href, "URL")}>
          Copy URL
        </button>
      </div>
      <output aria-live="polite" className="copy-status">
        {copyStatus}
      </output>
      <div className="detail-actions">
        <button className="primary" onClick={onAnalytics}>
          Analytics ↗
        </button>
        {onSettings && <button onClick={onSettings}>⚙ Settings</button>}
      </div>
    </div>
  );
}
function Timeline({ path }: { path: string }) {
  const [history, setHistory] = useState<HistoryResponse>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    setHistory(undefined);
    setError("");
    setLoading(true);
    api.history(path, "", controller.signal).then(
      (result) => {
        setHistory(result);
        setLoading(false);
      },
      (reason: Error) => {
        if (reason.name !== "AbortError") {
          setError(reason.message);
          setLoading(false);
        }
      },
    );
    return () => controller.abort();
  }, [path]);
  const loadMore = async () => {
    if (!history?.next_cursor || loadingMore) return;
    setLoadingMore(true);
    setError("");
    try {
      const next = await api.history(path, history.next_cursor);
      setHistory({
        available: next.available,
        entries: [...history.entries, ...next.entries],
        next_cursor: next.next_cursor,
      });
    } catch (reason) {
      setError(
        reason instanceof Error ? reason.message : "Couldn’t load history",
      );
    } finally {
      setLoadingMore(false);
    }
  };
  if (loading) return <div className="state loading">Loading timeline…</div>;
  if (error && !history)
    return <div className="state error">Timeline unavailable: {error}</div>;
  if (!history?.available)
    return (
      <p className="empty">
        History is not available for this knowledge source.
      </p>
    );
  if (!history.entries.length)
    return <p className="empty">No history for this file.</p>;
  return (
    <>
      <ol className="timeline">
        {history.entries.map((entry, index) => (
          <li key={`${entry.hash}-${entry.time}-${index}`}>
            <strong>{entry.action}</strong>
            <span>{entry.attribution}</span>
            <time dateTime={entry.time}>
              {new Date(entry.time).toLocaleString()}
            </time>
            <code>{entry.hash.slice(0, 9)}</code>
          </li>
        ))}
      </ol>
      {error && <p role="alert">Couldn’t load more history: {error}</p>}
      {history.next_cursor && (
        <button disabled={loadingMore} onClick={() => void loadMore()}>
          {loadingMore ? "Loading…" : "Load more"}
        </button>
      )}
    </>
  );
}
export function Details({
  file,
  ratio = 4,
  contextWindow,
  onCopy,
  onAnalytics,
  onSettings,
}: {
  file: FileResponse;
  ratio?: number;
  contextWindow: number;
  onCopy: (value: string) => void | Promise<void>;
  onAnalytics: () => void;
  onSettings?: () => void;
}) {
  const [tab, setTab] = useState<"info" | "timeline">("info");
  return (
    <>
      <div className="info-tabs" role="tablist">
        <button aria-selected={tab === "info"} onClick={() => setTab("info")}>
          Info
        </button>
        <button
          aria-selected={tab === "timeline"}
          onClick={() => setTab("timeline")}
        >
          Timeline
        </button>
      </div>
      {tab === "info" ? (
        <FileInfo
          {...{ file, ratio, contextWindow, onCopy, onAnalytics, onSettings }}
        />
      ) : (
        <Timeline path={file.path} />
      )}
    </>
  );
}
export function FileReader({
  path,
  session,
  mode,
  onMode,
  onFile,
  onAnalytics,
  ratio = 4,
  contextWindow,
  scrollPosition,
  onScroll,
}: {
  path: string;
  session: Session;
  mode: "preview" | "source";
  onMode: (mode: "preview" | "source") => void;
  onFile: (path: string) => void;
  onAnalytics: () => void;
  ratio?: number;
  contextWindow: number;
  scrollPosition: number;
  onScroll: (position: number) => void;
}) {
  const state = useAsync((signal) => api.file(path, signal), [path]);
  const scrollRef = useRef<HTMLDivElement>(null);
  const onScrollRef = useRef(onScroll);
  onScrollRef.current = onScroll;
  useEffect(() => {
    if (!state.data) return;
    const mobile = matchMedia("(max-width: 760px)").matches;
    if (!mobile) {
      if (scrollRef.current) scrollRef.current.scrollTop = scrollPosition;
      return;
    }
    window.scrollTo(0, scrollPosition);
    const trackWindow = () => onScrollRef.current(window.scrollY);
    window.addEventListener("scroll", trackWindow, { passive: true });
    return () => window.removeEventListener("scroll", trackWindow);
  }, [state.data, path]);
  if (state.loading)
    return (
      <div className="state loading" role="status">
        Loading document…
      </div>
    );
  if (state.error)
    return (
      <div className="state error" role="alert">
        <strong>Couldn’t open this file</strong>
        <p>{state.error.message}</p>
      </div>
    );
  const file = state.data;
  if (!file) return null;
  const copy = (value: string) => navigator.clipboard.writeText(value);
  return (
    <div className="file-layout">
      <section className="reader">
        <div className="reader-head">
          <strong>{basename(path)}</strong>
          <div className="view-toggle">
            <button
              aria-pressed={mode === "preview"}
              disabled={file.binary}
              onClick={() => onMode("preview")}
            >
              Preview
            </button>
            <button
              aria-pressed={mode === "source"}
              disabled={file.binary}
              onClick={() => onMode("source")}
            >
              Source
            </button>
          </div>
        </div>
        <div
          className="document-scroll"
          ref={scrollRef}
          onScroll={(event) => {
            if (!matchMedia("(max-width: 760px)").matches)
              onScroll(event.currentTarget.scrollTop);
          }}
        >
          {file.binary ? (
            <div className="binary-state">
              <strong>Preview unavailable</strong>
              <p>This binary file can’t be displayed in the dashboard.</p>
            </div>
          ) : mode === "source" ? (
            <pre className="source">
              <code>{file.source}</code>
            </pre>
          ) : (
            <Markdown {...{ file, session, onFile }} />
          )}
        </div>
      </section>
      <aside className="details-panel">
        <Details
          file={file}
          ratio={ratio}
          contextWindow={contextWindow}
          onCopy={copy}
          onAnalytics={onAnalytics}
        />
      </aside>
    </div>
  );
}
export function MobileDetails({
  path,
  session,
  mode = "preview",
  onMode = () => {},
  ratio = 4,
  contextWindow,
  onClose,
  onAnalytics,
  onSettings,
}: {
  path: string;
  session: Session;
  mode?: "preview" | "source";
  onMode?: (mode: "preview" | "source") => void;
  ratio?: number;
  contextWindow: number;
  onClose: () => void;
  onAnalytics: () => void;
  onSettings: () => void;
}) {
  const state = useAsync((signal) => api.file(path, signal), [path]);
  return (
    <Sheet title={basename(path)} onClose={onClose}>
      {state.loading ? (
        <div className="state loading">Loading details…</div>
      ) : state.error ? (
        <div className="state error">{state.error.message}</div>
      ) : (
        state.data && (
          <>
            <div className="view-toggle" aria-label="File view">
              <button
                aria-pressed={mode === "preview"}
                disabled={state.data.binary}
                onClick={() => onMode("preview")}
              >
                Preview
              </button>
              <button
                aria-pressed={mode === "source"}
                disabled={state.data.binary}
                onClick={() => onMode("source")}
              >
                Source
              </button>
            </div>
            <Details
              file={state.data}
              ratio={ratio}
              contextWindow={contextWindow}
              onCopy={(value) => navigator.clipboard.writeText(value)}
              onAnalytics={onAnalytics}
              onSettings={onSettings}
            />
          </>
        )
      )}
      <small className="identity-note">Signed in to {session.lore_path}</small>
    </Sheet>
  );
}
export function Sheet({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: React.ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const before = document.activeElement as HTMLElement;
    const scrollY = window.scrollY;
    const previous = {
      overflow: document.body.style.overflow,
      position: document.body.style.position,
      top: document.body.style.top,
      width: document.body.style.width,
    };
    document.body.style.overflow = "hidden";
    document.body.style.position = "fixed";
    document.body.style.top = `-${scrollY}px`;
    document.body.style.width = "100%";
    ref.current?.focus();
    return () => {
      document.body.style.overflow = previous.overflow;
      document.body.style.position = previous.position;
      document.body.style.top = previous.top;
      document.body.style.width = previous.width;
      window.scrollTo(0, scrollY);
      before?.focus();
    };
  }, []);
  const keyboard = (event: React.KeyboardEvent) => {
    if (event.key === "Escape") {
      onClose();
      return;
    }
    if (event.key !== "Tab" || !ref.current) return;
    const items = [
      ...ref.current.querySelectorAll<HTMLElement>(
        'button:not(:disabled),a[href],select,[tabindex]:not([tabindex="-1"])',
      ),
    ];
    if (!items.length) return;
    const first = items[0],
      last = items.at(-1)!;
    if (document.activeElement === ref.current) {
      event.preventDefault();
      (event.shiftKey ? last : first).focus();
    } else if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };
  return (
    <div
      className="sheet-backdrop"
      role="presentation"
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <section
        className="sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="sheet-title"
        tabIndex={-1}
        ref={ref}
        onKeyDown={keyboard}
      >
        <header>
          <h2 id="sheet-title">{title}</h2>
          <button className="close" onClick={onClose} aria-label="Close">
            ×
          </button>
        </header>
        {children}
      </section>
    </div>
  );
}
export type { HistoryResponse };
