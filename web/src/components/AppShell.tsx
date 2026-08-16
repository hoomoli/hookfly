import type { ReactNode } from "react";

interface AppShellProps {
  navigation: ReactNode;
  children: ReactNode;
}

export function AppShell({ navigation, children }: AppShellProps) {
  return (
    <div data-slot="app-shell" className="min-h-screen bg-background text-foreground lg:grid lg:grid-cols-[240px_minmax(0,1fr)]">
      <aside className="z-20 border-b border-border bg-muted/45 backdrop-blur-xl lg:sticky lg:top-0 lg:h-screen lg:border-r lg:border-b-0">
        {navigation}
      </aside>
      <main className="min-w-0">
        <div className="px-4 pb-8 sm:px-6 lg:px-8">{children}</div>
      </main>
    </div>
  );
}
