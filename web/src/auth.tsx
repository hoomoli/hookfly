import { useCallback, useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, getAuthSession, logout } from "./api";
import { EmptyState } from "./components/EmptyState";
import { Button } from "./components/ui/button";
import type { AuthSession } from "./types";

type AuthState =
  | { status: "loading" }
  | { status: "authenticated"; session: AuthSession }
  | { status: "denied" }
  | { status: "unavailable" }
  | { status: "logout_failed" }
  | { status: "signed_out" };

interface AuthGateProps {
  children: (session: AuthSession, signOut: () => Promise<void>) => ReactNode;
  navigate?: (url: string) => void;
}

function defaultNavigate(url: string) {
  window.location.assign(url);
}

function loginURL() {
  const returnTo = `${window.location.pathname}${window.location.search}${window.location.hash}`;
  return `/api/v1/auth/login?return_to=${encodeURIComponent(returnTo)}`;
}

export function AuthGate({ children, navigate = defaultNavigate }: AuthGateProps) {
  const { t } = useTranslation();
  const [state, setState] = useState<AuthState>({ status: "loading" });

  const resolveSession = useCallback(() => {
    setState({ status: "loading" });
    void getAuthSession().then(
      (session) => setState({ status: "authenticated", session }),
      (reason: unknown) => {
        if (reason instanceof ApiError && reason.status === 401) {
          navigate(loginURL());
          return;
        }
        if (reason instanceof ApiError && reason.status === 403) {
          setState({ status: "denied" });
          return;
        }
        setState({ status: "unavailable" });
      },
    );
  }, [navigate]);

  useEffect(resolveSession, [resolveSession]);

  const signOut = useCallback(async () => {
    try {
      await logout();
      setState({ status: "signed_out" });
    } catch {
      setState({ status: "logout_failed" });
    }
  }, []);

  if (state.status === "authenticated") return children(state.session, signOut);
  if (state.status === "denied") {
    return <EmptyState title={t("auth.deniedTitle")} description={t("auth.deniedDescription")} tone="destructive" />;
  }
  if (state.status === "unavailable") {
    return <EmptyState title={t("auth.unavailableTitle")} description={t("auth.unavailableDescription")} tone="warning" action={<Button onClick={resolveSession}>{t("common.retry")}</Button>} />;
  }
  if (state.status === "logout_failed") {
    return <EmptyState title={t("auth.logoutFailedTitle")} description={t("auth.logoutFailedDescription")} tone="warning" action={<Button onClick={() => void signOut()}>{t("common.retry")}</Button>} />;
  }
  if (state.status === "signed_out") {
    return <EmptyState title={t("auth.signedOutTitle")} description={t("auth.signedOutDescription")} action={<Button onClick={() => navigate(loginURL())}>{t("auth.signIn")}</Button>} />;
  }
  return <div className="grid min-h-screen place-items-center text-sm text-muted-foreground" role="status">{t("auth.checking")}</div>;
}
