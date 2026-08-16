import { useCallback, useEffect, useRef, useState } from "react";
import { i18n } from "../i18n";

interface ActiveResponse { active: boolean }

export interface PollingState<T> {
  data: T | null;
  error: Error | null;
  loading: boolean;
  refresh: () => void;
}

export function useActivePolling<T extends ActiveResponse>(load: () => Promise<T>): PollingState<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const [loading, setLoading] = useState(true);
  const runRef = useRef<() => void>(() => undefined);
  const activeRef = useRef(false);

  const refresh = useCallback(() => runRef.current(), []);

  useEffect(() => {
    let disposed = false;
    let requestVersion = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const clearTimer = () => {
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
    };

    const run = () => {
      clearTimer();
      const version = ++requestVersion;
      setLoading(true);
      void load().then(
        (next) => {
          if (disposed || version !== requestVersion) return;
          setData(next);
          activeRef.current = next.active;
          setError(null);
          setLoading(false);
          if (next.active && document.visibilityState === "visible") {
            timer = setTimeout(run, 1_000);
          }
        },
        (reason: unknown) => {
          if (disposed || version !== requestVersion) return;
          setError(reason instanceof Error ? reason : new Error(i18n.t("errors.requestFailed")));
          setLoading(false);
          if (activeRef.current && document.visibilityState === "visible") {
            timer = setTimeout(run, 1_000);
          }
        },
      );
    };

    const onVisibility = () => {
      clearTimer();
      if (document.visibilityState === "visible") run();
      else requestVersion += 1;
    };

    runRef.current = run;
    document.addEventListener("visibilitychange", onVisibility);
    if (document.visibilityState === "visible") run();
    return () => {
      disposed = true;
      requestVersion += 1;
      clearTimer();
      document.removeEventListener("visibilitychange", onVisibility);
      runRef.current = () => undefined;
    };
  }, [load]);

  return { data, error, loading, refresh };
}
