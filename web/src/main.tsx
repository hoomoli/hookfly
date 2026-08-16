import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { TooltipProvider } from "./components/ui/tooltip";
import { AppThemeProvider } from "./theme";
import "./i18n";
import "./styles.css";

const root = document.getElementById("app");
if (!root) throw new Error("missing application root");
createRoot(root).render(
  <StrictMode>
    <AppThemeProvider>
      <TooltipProvider delayDuration={300}>
        <App />
      </TooltipProvider>
    </AppThemeProvider>
  </StrictMode>,
);
