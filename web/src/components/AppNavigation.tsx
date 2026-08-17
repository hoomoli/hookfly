import { useTranslation } from "react-i18next";
import type { AuthSession, Repository, RepositorySelection } from "../types";
import { Button } from "./ui/button";
import { UserMenu } from "./UserMenu";
import { siteName as configuredSiteName } from "../site-config";

export type AppPage = "ledger" | "targets" | "connections";

interface Props {
  repositories: Repository[];
  selected: RepositorySelection;
  page: AppPage;
  onSelectRepository: (selection: RepositorySelection) => void;
  onSelectPage: (page: AppPage) => void;
  session: AuthSession;
  onSignOut: () => Promise<void> | void;
  siteName?: string;
}

function isSelected(current: RepositorySelection, candidate: RepositorySelection) {
  return current.kind === candidate.kind && (candidate.kind !== "repository" || (
    current.kind === "repository"
    && current.sourceID === candidate.sourceID
    && current.repositoryID === candidate.repositoryID
  ));
}

const activeClass = "h-11 min-w-11 shrink-0 justify-between bg-primary/10 px-3 text-primary hover:bg-primary/15 lg:w-full";
const idleClass = "h-11 min-w-11 shrink-0 justify-between px-3 text-muted-foreground hover:text-foreground lg:w-full";

export function AppNavigation({ repositories, selected, page, onSelectRepository, onSelectPage, session, onSignOut, siteName = configuredSiteName }: Props) {
  const { t } = useTranslation();
  return (
    <nav className="flex h-full min-w-0 items-center gap-4 overflow-x-auto px-3 py-2 lg:flex-col lg:items-stretch lg:overflow-x-visible lg:px-3 lg:py-5" aria-label={t("navigation.ariaLabel")}>
      <div className="hidden items-center gap-3 px-2 pb-5 lg:flex">
        <span className="grid size-9 place-items-center rounded-[11px] bg-primary text-xs font-semibold text-primary-foreground shadow-sm" aria-hidden="true">HF</span>
        <span><strong className="block text-sm font-semibold tracking-[-0.01em]">{siteName}</strong><small className="mt-0.5 block text-[10px] text-muted-foreground">{t("navigation.deploymentLedger")}</small></span>
      </div>

      <section className="flex min-w-0 shrink-0 items-center gap-1 lg:block" aria-labelledby="events-navigation-title">
        <div className="hidden px-2 pb-2 lg:block">
          <p id="events-navigation-title" className="text-[10px] font-medium tracking-[0.12em] text-muted-foreground">{t("navigation.events")}</p>
          <p className="mt-0.5 text-[10px] text-muted-foreground/70">{t("navigation.repositories")}</p>
        </div>
        <div className="flex min-w-0 gap-1 lg:block lg:space-y-1">
          {[{ kind: "all" } as const, { kind: "unmatched" } as const].map((selection) => {
            const active = page === "ledger" && isSelected(selected, selection);
            return (
              <Button key={selection.kind} type="button" variant="ghost" size="touch" className={active ? activeClass : idleClass} aria-pressed={active} onClick={() => onSelectRepository(selection)}>
                <span className="truncate">{t(selection.kind === "all" ? "common.all" : "common.unmatched")}</span>
              </Button>
            );
          })}
          {repositories.map((repository) => {
            const selection: RepositorySelection = { kind: "repository", sourceID: repository.source_id, repositoryID: repository.id };
            const active = page === "ledger" && isSelected(selected, selection);
            return (
              <Button key={`${repository.provider}:${repository.source_id}:${repository.id}`} type="button" variant="ghost" size="touch" className={active ? activeClass : idleClass} aria-label={`${repository.id} · ${repository.provider} · ${repository.source_id}`} aria-pressed={active} onClick={() => onSelectRepository(selection)}>
                <span className="min-w-0 text-left"><span className="block truncate text-xs">{repository.id}</span><small className="block truncate text-[10px] opacity-65">{repository.provider} · {repository.source_id}</small></span>
              </Button>
            );
          })}
        </div>
      </section>

      <section className="flex shrink-0 items-center gap-1 lg:mt-6 lg:block" aria-labelledby="configuration-navigation-title">
        <p id="configuration-navigation-title" className="hidden px-2 pb-2 text-[10px] font-medium tracking-[0.12em] text-muted-foreground lg:block">{t("navigation.configuration")}</p>
        <Button type="button" variant="ghost" size="touch" className={page === "targets" ? activeClass : idleClass} aria-pressed={page === "targets"} onClick={() => onSelectPage("targets")}>
          <span className="truncate">{t("navigation.targets")}</span>
        </Button>
        <Button type="button" variant="ghost" size="touch" className={page === "connections" ? activeClass : idleClass} aria-pressed={page === "connections"} onClick={() => onSelectPage("connections")}>
          <span className="truncate">{t("navigation.connections")}</span>
        </Button>
      </section>

      <div className="ml-auto shrink-0 lg:mt-auto lg:ml-0">
        <UserMenu session={session} onSignOut={onSignOut} />
      </div>
    </nav>
  );
}
