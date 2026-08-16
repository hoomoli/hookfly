import { LogOut, UserRound } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { AuthSession } from "../types";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";

interface UserMenuProps {
  session: AuthSession;
  onSignOut: () => Promise<void> | void;
}

function initials(name: string) {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "HF";
  if (parts.length === 1) return Array.from(parts[0]).slice(0, 2).join("").toUpperCase();
  return `${Array.from(parts[0])[0] ?? ""}${Array.from(parts.at(-1) ?? "")[0] ?? ""}`.toUpperCase();
}

export function UserMenu({ session, onSignOut }: UserMenuProps) {
  const { t } = useTranslation();
  const { user } = session;
  const providerLabel = user.provider === "authentik" ? "Authentik" : t("auth.localProvider");
  const showUsername = Boolean(user.username && user.username !== user.display_name);

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          className="flex h-12 min-w-12 items-center gap-2 rounded-xl border border-border/80 bg-card/70 px-2 text-left shadow-xs transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring lg:w-full"
          aria-label={t("auth.accountMenu", { name: user.display_name })}
        >
          <span className="grid size-8 shrink-0 place-items-center rounded-[10px] bg-primary/12 text-[11px] font-semibold tracking-wide text-primary" aria-hidden="true">{initials(user.display_name)}</span>
          <span className="hidden min-w-0 lg:block">
            <strong className="block truncate text-xs font-semibold text-foreground">{user.display_name}</strong>
            <small className="mt-0.5 block truncate text-[10px] text-muted-foreground">{user.provider === "local" ? t("auth.localProvider") : t("auth.role")}</small>
          </span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" side="right" className="w-60 lg:ml-1">
        <DropdownMenuLabel>
          <div className="flex items-center gap-3">
            <span className="grid size-9 shrink-0 place-items-center rounded-[11px] bg-primary/12 text-xs font-semibold text-primary" aria-hidden="true">{initials(user.display_name)}</span>
            <span className="min-w-0">
              <strong className="block truncate text-sm font-semibold">{user.display_name}</strong>
              {showUsername && <small className="block truncate text-xs font-normal text-muted-foreground">{user.username}</small>}
            </span>
          </div>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="flex items-center gap-2 text-xs font-normal text-muted-foreground">
          <UserRound className="size-3.5" aria-hidden="true" />
          <span>{providerLabel}</span>
        </DropdownMenuLabel>
        {user.provider === "authentik" && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => void onSignOut()}>
              <LogOut className="size-4" aria-hidden="true" />
              {t("auth.signOut")}
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
