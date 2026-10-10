import { useEffect, useId, useMemo, useRef, useState } from "react";
import { api } from "./api";
import { rankPaths, type Match } from "./fuzzy";
import { useModalLock } from "./hooks";
import { FileIcon, SearchIcon } from "./icons";
import type { FilesResponse } from "./types";

const RESULT_LIMIT = 100;

// Listings are cached per identity and scope so reopening the finder is
// instant while a fresh listing loads behind it. Keying by identity keeps one
// signed-in user's paths from flashing for the next one in the same tab.
const listings = new Map<string, FilesResponse>();

/**
 * Loads the searchable file list for one scope. Today the only scope is the
 * workspace root; a per-docset finder passes a docset root here instead.
 */
export function useFileIndex(identity: string, scope: string) {
  const key = `${identity}\0${scope}`;
  const [index, setIndex] = useState(() => listings.get(key));
  const [error, setError] = useState<Error>();
  useEffect(() => {
    const controller = new AbortController();
    setIndex(listings.get(key));
    setError(undefined);
    api.files(scope, controller.signal).then(
      (data) => {
        if (controller.signal.aborted) return;
        listings.set(key, data);
        setIndex(data);
      },
      (err: Error) => {
        if (!controller.signal.aborted) setError(err);
      },
    );
    return () => controller.abort();
  }, [key, scope]);
  return { index, error };
}

export const finderShortcut = /Mac|iPhone|iPad/.test(navigator.platform)
  ? "⌘P"
  : "Ctrl+P";

export function isFinderShortcut(event: KeyboardEvent) {
  return (
    (event.metaKey || event.ctrlKey) &&
    !event.altKey &&
    !event.shiftKey &&
    event.key.toLowerCase() === "p"
  );
}

export function FuzzyFinder({
  identity,
  scope,
  recent,
  onFile,
  onClose,
}: {
  identity: string;
  scope: string;
  /** Paths to offer before the user types, most recent last. */
  recent: string[];
  onFile: (path: string) => void;
  onClose: () => void;
}) {
  const { index, error } = useFileIndex(identity, scope);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const listId = useId();
  useModalLock(input);
  const paths = useMemo(
    () => index?.entries.map((entry) => entry.path) ?? [],
    [index],
  );
  const results = useMemo(() => {
    if (query.trim()) return rankPaths(paths, query, RESULT_LIMIT);
    const known = new Set(paths);
    const starters = [...recent].reverse().filter((path) => known.has(path));
    return rankPaths(starters.length ? starters : paths, "", RESULT_LIMIT);
  }, [paths, recent, query]);
  const current = results[Math.min(active, results.length - 1)];
  useEffect(() => {
    list.current
      ?.querySelector<HTMLElement>('[aria-selected="true"]')
      ?.scrollIntoView?.({ block: "nearest" });
  }, [active, results]);
  const choose = (match: Match | undefined) => {
    if (match) onFile(match.path);
  };
  const keyboard = (event: React.KeyboardEvent) => {
    switch (event.key) {
      case "Escape":
        onClose();
        break;
      case "ArrowDown":
        setActive((i) => Math.min(i + 1, results.length - 1));
        break;
      case "ArrowUp":
        setActive((i) => Math.max(i - 1, 0));
        break;
      case "Home":
        setActive(0);
        break;
      case "End":
        setActive(results.length - 1);
        break;
      case "Enter":
        choose(current);
        break;
      default:
        return;
    }
    event.preventDefault();
  };
  const count = index?.entries.length ?? 0;
  const status = error
    ? "Couldn’t load the file list."
    : !index
      ? "Loading files…"
      : index.truncated
        ? `Searching the first ${count.toLocaleString()} files; narrow the scope to see the rest.`
        : query.trim()
          ? `${results.length === RESULT_LIMIT ? `Top ${RESULT_LIMIT} of ` : ""}${results.length} match${results.length === 1 ? "" : "es"}`
          : `${count.toLocaleString()} files. Type to search.`;
  return (
    <div
      className="sheet-backdrop finder-backdrop"
      role="presentation"
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <section
        className="finder"
        role="dialog"
        aria-modal="true"
        aria-label="Find file"
        onKeyDown={keyboard}
      >
        <div className="finder-input">
          <SearchIcon />
          <input
            ref={input}
            type="text"
            role="combobox"
            aria-label="Find file"
            aria-expanded="true"
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={current ? optionId(listId, current.path) : undefined}
            autoComplete="off"
            spellCheck={false}
            placeholder="Find file by name or path"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setActive(0);
            }}
          />
          <kbd aria-hidden="true">esc</kbd>
        </div>
        <div
          className="finder-results"
          role="listbox"
          id={listId}
          aria-label="Matching files"
          ref={list}
        >
          {results.map((match) => (
            <div
              key={match.path}
              id={optionId(listId, match.path)}
              role="option"
              aria-selected={match === current}
              onMouseMove={() => {
                const i = results.indexOf(match);
                if (i !== active) setActive(i);
              }}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => choose(match)}
            >
              <FileIcon />
              <Highlighted match={match} />
            </div>
          ))}
          {index && query.trim() && results.length === 0 && (
            <p className="finder-empty">No files match “{query.trim()}”.</p>
          )}
        </div>
        <footer className="finder-status" role="status">
          <span>{status}</span>
          <span className="finder-keys" aria-hidden="true">
            <kbd>↑</kbd>
            <kbd>↓</kbd> move <kbd>↵</kbd> open
          </span>
        </footer>
      </section>
    </div>
  );
}

const optionId = (listId: string, path: string) =>
  `${listId}-${encodeURIComponent(path)}`;

function Highlighted({ match }: { match: Match }) {
  const cut = match.path.lastIndexOf("/") + 1;
  const name = match.path.slice(cut);
  const dir = match.path.slice(0, cut);
  const marks = new Set(match.positions);
  // Positions are UTF-16 indices (they come from indexOf), so slice by code
  // unit and merge adjacent hits into one <mark> to keep surrogate pairs whole.
  const render = (text: string, offset: number) => {
    const parts: React.ReactNode[] = [];
    let start = 0;
    for (let i = 1; i <= text.length; i++) {
      if (i < text.length && marks.has(offset + i) === marks.has(offset + start))
        continue;
      const chunk = text.slice(start, i);
      parts.push(
        marks.has(offset + start) ? <mark key={start}>{chunk}</mark> : chunk,
      );
      start = i;
    }
    return parts;
  };
  return (
    <span className="finder-match">
      <span className="finder-name">{render(name, cut)}</span>
      <span className="finder-dir">{render(dir, 0)}</span>
    </span>
  );
}
