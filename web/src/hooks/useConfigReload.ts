import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, getConfigStatus, reloadConfig } from "../api";
import { i18n } from "../i18n";
import type { ConfigStatus } from "../types";

export function configReloadLabel(status: ConfigStatus | null): string {
  switch (status?.state) {
    case "idle": return i18n.t("reload.idle");
    case "validating": return i18n.t("reload.validating");
    case "waiting": return i18n.t("reload.waiting", { count: status.active_deliveries });
    case "applying": return i18n.t("reload.applying");
    case "draining": return i18n.t("reload.draining", { count: status.queued_events });
    default: return i18n.t("reload.reading");
  }
}

export interface ConfigReloadController {
  status: ConfigStatus | null;
  error: Error | null;
  disabled: boolean;
  submit: () => void;
}

export function useConfigReload(onIdle: () => void): ConfigReloadController {
  const [status, setStatus] = useState<ConfigStatus | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const statusRef = useRef<ConfigStatus | null>(null);
  const submittingRef = useRef(false);
  const cycleRef = useRef(false);
  const runStatusRef = useRef<() => void>(() => undefined);
  const onIdleRef = useRef(onIdle);
  onIdleRef.current = onIdle;

  useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const schedule = () => {
      if (timer !== undefined) clearTimeout(timer);
      timer = setTimeout(run, 1_000);
    };

    const run = () => {
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
      void getConfigStatus().then(
        (next) => {
          if (disposed) return;
          const completedCycle = next.state === "idle" && cycleRef.current;
          statusRef.current = next;
          setStatus(next);
          setError(null);
          if (next.state === "idle") {
            cycleRef.current = false;
            submittingRef.current = false;
            setSubmitting(false);
            if (completedCycle) onIdleRef.current();
          } else {
            cycleRef.current = true;
            schedule();
          }
        },
        (reason: unknown) => {
          if (disposed) return;
          setError(reason instanceof Error ? reason : new Error(i18n.t("errors.configStatusFailed")));
          schedule();
        },
      );
    };

    runStatusRef.current = run;
    run();
    return () => {
      disposed = true;
      if (timer !== undefined) clearTimeout(timer);
      runStatusRef.current = () => undefined;
    };
  }, []);

  const submit = useCallback(() => {
    if (submittingRef.current || statusRef.current?.state !== "idle") return;
    submittingRef.current = true;
    cycleRef.current = true;
    setSubmitting(true);
    setError(null);
    void reloadConfig().then(
      () => runStatusRef.current(),
      (reason: unknown) => {
        if (reason instanceof ApiError && reason.status === 422 && reason.code === "invalid_config") {
          cycleRef.current = false;
          submittingRef.current = false;
          setSubmitting(false);
          setError(reason);
          return;
        }
        setError(reason instanceof Error ? reason : new Error(i18n.t("errors.reloadUnknown")));
        runStatusRef.current();
      },
    );
  }, []);

  return {
    status,
    error,
    disabled: submitting || status === null || status.state !== "idle",
    submit,
  };
}
