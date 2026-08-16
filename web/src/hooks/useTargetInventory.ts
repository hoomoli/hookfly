import { useCallback, useEffect, useRef, useState } from "react";
import { listTargets } from "../api";
import type { TargetInventory } from "../types";

export interface TargetInventoryState {
  data: TargetInventory | null;
  loading: boolean;
  error: string | null;
  refresh: () => void;
}

export function useTargetInventory(enabled: boolean): TargetInventoryState {
  const [data, setData] = useState<TargetInventory | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [request, setRequest] = useState(0);
  const completedRequest = useRef<number | null>(null);

  useEffect(() => {
    if (!enabled || completedRequest.current === request) return;
    let active = true;
    setLoading(true);
    setError(null);
    void listTargets().then(
      (next) => {
        if (!active) return;
        completedRequest.current = request;
        setData(next);
        setLoading(false);
      },
      (reason: unknown) => {
        if (!active) return;
        completedRequest.current = request;
        setError(reason instanceof Error ? reason.message : String(reason));
        setLoading(false);
      },
    );
    return () => {
      active = false;
    };
  }, [enabled, request]);

  const refresh = useCallback(() => setRequest((value) => value + 1), []);
  return { data, loading, error, refresh };
}
