import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { listRepositories } from "./api";
import { AppNavigation, type AppPage } from "./components/AppNavigation";
import { AppShell } from "./components/AppShell";
import { NotificationCenter } from "./components/NotificationCenter";
import { SettingsCenter } from "./components/SettingsCenter";
import { useNotifications } from "./hooks/useNotifications";
import { ConnectionsPage } from "./pages/ConnectionsPage";
import { LedgerPage } from "./pages/LedgerPage";
import { TargetsPage } from "./pages/TargetsPage";
import { useTargetInventory } from "./hooks/useTargetInventory";
import type { AuthSession, Repository, RepositorySelection } from "./types";
import { DisplaySettingsProvider } from "./display-settings";
import { AuthGate } from "./auth";

function currentSearch() {
  return window.location.search;
}

function normalizedSearch(value: string) {
  const query = new URLSearchParams(value);
  if (!query.get("source") || !query.get("repository")) {
    query.delete("source");
    query.delete("repository");
  }
  return query.size ? `?${query.toString()}` : "";
}

function selectionFrom(query: URLSearchParams): RepositorySelection {
  if (query.get("unmatched") === "true") return { kind: "unmatched" };
  const sourceID = query.get("source");
  const repositoryID = query.get("repository");
  return sourceID && repositoryID ? { kind: "repository", sourceID, repositoryID } : { kind: "all" };
}

function AppContent({ session, signOut }: { session: AuthSession; signOut: () => Promise<void> }) {
  const { t } = useTranslation();
  const [search, setSearch] = useState(currentSearch);
  const [repositories, setRepositories] = useState<Repository[]>([]);
  const [repositoryError, setRepositoryError] = useState<string | null>(null);
  const [historyRevision, setHistoryRevision] = useState(0);
  const query = useMemo(() => new URLSearchParams(search), [search]);
  const selection = selectionFrom(query);
  const page: AppPage = query.get("view") === "connections" ? "connections" : query.get("view") === "targets" ? "targets" : "ledger";
  const targetInventory = useTargetInventory(page !== "connections");
  const openNotificationEvent = useCallback((eventID: string) => {
    const next = new URLSearchParams(window.location.search);
    next.delete("view");
    next.set("id", eventID);
    const nextSearch = `?${next.toString()}`;
    window.history.replaceState(null, "", `${window.location.pathname}${nextSearch}`);
    setSearch(nextSearch);
  }, []);
  const notifications = useNotifications(openNotificationEvent, repositories);
  const onHistoryCleared = useCallback(() => {
    const next = new URLSearchParams(window.location.search);
    next.delete("id");
    next.delete("page");
    const nextSearch = next.size ? `?${next.toString()}` : "";
    window.history.replaceState(null, "", `${window.location.pathname}${nextSearch}`);
    setSearch(nextSearch);
    setHistoryRevision((revision) => revision + 1);
  }, []);
  const globalActions = <><SettingsCenter controller={notifications} repositories={repositories} onHistoryCleared={onHistoryCleared} /><NotificationCenter controller={notifications} onOpenEvent={openNotificationEvent} /></>;

  useEffect(() => {
    const onPopState = () => setSearch(currentSearch());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  useEffect(() => {
    const normalized = normalizedSearch(search);
    if (normalized !== search) {
      window.history.replaceState(null, "", `${window.location.pathname}${normalized}`);
      setSearch(normalized);
    }
  }, [search]);

  const replaceQuery = useCallback((next: URLSearchParams) => {
    const nextSearch = next.size ? `?${next.toString()}` : "";
    window.history.replaceState(null, "", `${window.location.pathname}${nextSearch}`);
    setSearch(nextSearch);
  }, []);

  const refreshRepositories = useCallback(() => {
    void listRepositories().then(
      (items) => {
        setRepositories(items);
        setRepositoryError(null);
        const next = new URLSearchParams(window.location.search);
        const selectedSource = next.get("source");
        const selectedRepository = next.get("repository");
        if ((selectedSource || selectedRepository) && !items.some((repository) => repository.source_id === selectedSource && repository.id === selectedRepository)) {
          next.delete("source");
          next.delete("repository");
          replaceQuery(next);
        }
      },
      (reason: unknown) => setRepositoryError(reason instanceof Error ? reason.message : t("errors.repositoryLoadFailed")),
    );
  }, [replaceQuery, t]);

  useEffect(refreshRepositories, [refreshRepositories]);

  const selectRepository = (nextSelection: RepositorySelection) => {
    const next = new URLSearchParams(window.location.search);
    next.delete("view");
    next.delete("source");
    next.delete("repository");
    next.delete("unmatched");
    next.delete("page");
    if (nextSelection.kind === "repository") {
      next.set("source", nextSelection.sourceID);
      next.set("repository", nextSelection.repositoryID);
    }
    if (nextSelection.kind === "unmatched") next.set("unmatched", "true");
    replaceQuery(next);
  };

  const selectPage = (nextPage: AppPage) => {
    const next = new URLSearchParams(window.location.search);
    if (nextPage === "connections" || nextPage === "targets") next.set("view", nextPage);
    else next.delete("view");
    replaceQuery(next);
  };

  return (
    <AppShell navigation={(
      <AppNavigation
        repositories={repositories}
        selected={selection}
        page={page}
        onSelectRepository={selectRepository}
        onSelectPage={selectPage}
        session={session}
        onSignOut={signOut}
      />
    )}>
      {page === "connections" ? <ConnectionsPage globalActions={globalActions} /> : page === "targets" ? <TargetsPage inventory={targetInventory} globalActions={globalActions} /> : (
        <LedgerPage
          globalActions={globalActions}
          search={search}
          repositories={repositories}
          repositoryError={repositoryError}
          selection={selection}
          replaceQuery={replaceQuery}
          refreshRepositories={refreshRepositories}
          targets={targetInventory.data?.targets ?? []}
          targetRefreshedAt={targetInventory.data?.refreshed_at}
          historyRevision={historyRevision}
        />
      )}
    </AppShell>
  );
}

export function App() {
  return (
    <DisplaySettingsProvider>
      <AuthGate>{(session, signOut) => <AppContent session={session} signOut={signOut} />}</AuthGate>
    </DisplaySettingsProvider>
  );
}
