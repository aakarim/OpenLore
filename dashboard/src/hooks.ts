import { useCallback, useEffect, useState, type RefObject } from "react";

/**
 * Locks page scroll while a modal is mounted, moves focus into it, and
 * restores both when it unmounts. `initial` is the element to focus.
 */
export function useModalLock(initial: RefObject<HTMLElement | null>) {
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
    initial.current?.focus();
    return () => {
      document.body.style.overflow = previous.overflow;
      document.body.style.position = previous.position;
      document.body.style.top = previous.top;
      document.body.style.width = previous.width;
      window.scrollTo(0, scrollY);
      before?.focus();
    };
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
