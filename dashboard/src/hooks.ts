import { useCallback, useEffect, useState } from "react";

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
