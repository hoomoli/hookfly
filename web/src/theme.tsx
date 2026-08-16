import { ThemeProvider } from "next-themes";
import type { ReactNode } from "react";

export const THEME_STORAGE_KEY = "hookfly-appearance";

export function AppThemeProvider({ children }: { children: ReactNode }) {
  return (
    <ThemeProvider
      attribute="class"
      defaultTheme="system"
      enableSystem
      storageKey={THEME_STORAGE_KEY}
      disableTransitionOnChange
    >
      {children}
    </ThemeProvider>
  );
}
