import { useCallback, useEffect, useState, type RefObject } from "react";

// Modals can nest (the file finder opens over a mobile sheet), so the page
// snapshot belongs to the outermost lock: only the first lock takes it and
// only the last release restores it. Each lock remembers what was focused
// when it opened; a release hands focus back to the newest of those that is
// still in the document, so closing every modal at once lands on whatever
// the outermost one covered rather than on an unmounted element.
type ModalLock = { before: (Element | null)[] };
const locks: ModalLock[] = [];
let page:
  | { overflow: string; position: string; top: string; width: string; scrollY: number }
  | undefined;

function acquireModalLock(): ModalLock {
  const lock = { before: [document.activeElement] };
  if (!page) {
    page = {
      overflow: document.body.style.overflow,
      position: document.body.style.position,
      top: document.body.style.top,
      width: document.body.style.width,
      scrollY: window.scrollY,
    };
    document.body.style.overflow = "hidden";
    document.body.style.position = "fixed";
    document.body.style.top = `-${page.scrollY}px`;
    document.body.style.width = "100%";
  }
  locks.push(lock);
  return lock;
}

function releaseModalLock(lock: ModalLock) {
  const index = locks.indexOf(lock);
  if (index < 0) return;
  // A lock released underneath a newer one has nothing to do: that lock
  // still owns scroll and focus, and inherits this one's return targets.
  if (index < locks.length - 1) {
    locks[index + 1].before.push(...lock.before);
    locks.splice(index, 1);
    return;
  }
  locks.pop();
  if (!locks.length && page) {
    document.body.style.overflow = page.overflow;
    document.body.style.position = page.position;
    document.body.style.top = page.top;
    document.body.style.width = page.width;
    window.scrollTo(0, page.scrollY);
    page = undefined;
  }
  const target = lock.before.find((element) => element?.isConnected);
  if (target instanceof HTMLElement) target.focus();
}

/**
 * Locks page scroll while a modal is mounted, moves focus into it, and
 * restores both when it unmounts. `initial` is the element to focus.
 */
export function useModalLock(initial: RefObject<HTMLElement | null>) {
  useEffect(() => {
    const lock = acquireModalLock();
    initial.current?.focus();
    return () => releaseModalLock(lock);
    // Runs once per mount; the ref is stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
}

export type AsyncState<T> = { data?: T; error?: Error; loading: boolean };
export function useAsync<T>(
  load: (signal: AbortSignal) => Promise<T>,
  dependencies: unknown[],
  enabled = true,
  retainWhileLoading = false,
) {
  const [state, setState] = useState<AsyncState<T>>({ loading: enabled });
  const [revision, setRevision] = useState(0);
  const refresh = useCallback(() => setRevision((value) => value + 1), []);
  useEffect(() => {
    if (!enabled) {
      setState({ loading: false });
      return;
    }
    const controller = new AbortController();
    setState((previous) => ({
      loading: true,
      data: retainWhileLoading ? previous.data : undefined,
      error: retainWhileLoading ? previous.error : undefined,
    }));
    load(controller.signal).then(
      (data) => {
        if (!controller.signal.aborted) setState({ data, loading: false });
      },
      (error) => {
        if (!controller.signal.aborted)
          setState((previous) => ({
            error,
            loading: false,
            data: retainWhileLoading ? previous.data : undefined,
          }));
      },
    );
    return () => controller.abort();
    // load is intentionally supplied inline and represented by dependencies.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...dependencies, enabled, revision, retainWhileLoading]);
  return { ...state, refresh };
}
