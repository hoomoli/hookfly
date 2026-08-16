import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { i18n } from "./i18n";

let systemDark = false;
const mediaListeners = new Set<(event: MediaQueryListEvent) => void>();

export function setSystemDark(value: boolean) {
  systemDark = value;
  const event = { matches: value, media: "(prefers-color-scheme: dark)" } as MediaQueryListEvent;
  for (const listener of mediaListeners) listener(event);
}

Object.defineProperty(window, "matchMedia", {
  configurable: true,
  value: (query: string): MediaQueryList => ({
    matches: query === "(prefers-color-scheme: dark)" && systemDark,
    media: query,
    onchange: null,
    addListener: (listener) => { if (listener) mediaListeners.add(listener); },
    removeListener: (listener) => { if (listener) mediaListeners.delete(listener); },
    addEventListener: (_type: string, listener: EventListenerOrEventListenerObject | null) => {
      if (typeof listener === "function") mediaListeners.add(listener as (event: MediaQueryListEvent) => void);
    },
    removeEventListener: (_type: string, listener: EventListenerOrEventListenerObject | null) => {
      if (typeof listener === "function") mediaListeners.delete(listener as (event: MediaQueryListEvent) => void);
    },
    dispatchEvent: () => true,
  }),
});

Object.defineProperties(HTMLElement.prototype, {
  hasPointerCapture: { configurable: true, value: () => false },
  releasePointerCapture: { configurable: true, value: () => undefined },
  scrollIntoView: { configurable: true, value: () => undefined },
});

afterEach(async () => {
  cleanup();
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
  localStorage.clear();
  document.documentElement.className = "";
  delete document.documentElement.dataset.dataFontSize;
  systemDark = false;
  mediaListeners.clear();
  window.history.replaceState(null, "", "/");
});
