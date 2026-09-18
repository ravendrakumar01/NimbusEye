import { useCallback, useEffect, useRef, useState } from "react";

export interface AsyncState<T> {
  data: T | undefined;
  error: Error | undefined;
  loading: boolean;
  /** True only on the first load, so refreshes do not blank the screen. */
  initialLoading: boolean;
  reload: () => void;
}

/**
 * Runs an async function and tracks its state.
 *
 * Two behaviours worth noting:
 *  - Stale responses are discarded. Without the generation counter, typing in a
 *    filter box can leave the results of an earlier, slower request on screen.
 *  - Previous data is kept while refetching, so polling does not flash the
 *    dashboard between renders.
 */
export function useAsync<T>(fn: () => Promise<T>, deps: unknown[]): AsyncState<T> {
  const [data, setData] = useState<T | undefined>(undefined);
  const [error, setError] = useState<Error | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [initialLoading, setInitialLoading] = useState(true);
  const [nonce, setNonce] = useState(0);
  const generation = useRef(0);

  useEffect(() => {
    const gen = ++generation.current;
    let cancelled = false;
    setLoading(true);

    fn()
      .then((res) => {
        if (cancelled || gen !== generation.current) return;
        setData(res);
        setError(undefined);
      })
      .catch((e: unknown) => {
        if (cancelled || gen !== generation.current) return;
        setError(e instanceof Error ? e : new Error(String(e)));
      })
      .finally(() => {
        if (cancelled || gen !== generation.current) return;
        setLoading(false);
        setInitialLoading(false);
      });

    return () => {
      cancelled = true;
    };
    // fn is intentionally excluded: callers pass an inline closure, and deps
    // describes what it actually depends on.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  return { data, error, loading, initialLoading, reload };
}

/**
 * Calls `fn` on an interval while the tab is visible.
 *
 * Pausing on a hidden tab matters more than it looks: a dashboard left open on a
 * second monitor overnight would otherwise issue tens of thousands of requests
 * and, once this talks to real cloud APIs, consume rate limit for nobody's
 * benefit.
 */
export function usePolling(fn: () => void, intervalMs: number, enabled = true) {
  const fnRef = useRef(fn);
  fnRef.current = fn;

  useEffect(() => {
    if (!enabled || intervalMs <= 0) return;
    let timer: number | undefined;

    const start = () => {
      stop();
      timer = window.setInterval(() => {
        if (document.visibilityState === "visible") fnRef.current();
      }, intervalMs);
    };
    const stop = () => {
      if (timer !== undefined) window.clearInterval(timer);
      timer = undefined;
    };
    const onVisibility = () => {
      if (document.visibilityState === "visible") {
        fnRef.current();
        start();
      } else {
        stop();
      }
    };

    start();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      stop();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [intervalMs, enabled]);
}

/** Debounces a rapidly changing value, for search inputs. */
export function useDebounced<T>(value: T, delayMs = 300): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(value), delayMs);
    return () => window.clearTimeout(t);
  }, [value, delayMs]);
  return debounced;
}
