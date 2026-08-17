export const DEFAULT_SITE_NAME = "Hookfly";

interface RuntimeConfig {
  siteName?: unknown;
}

declare global {
  interface Window {
    HOOKFLY_CONFIG?: RuntimeConfig;
  }
}

export function readSiteName(config: RuntimeConfig | undefined): string {
  if (typeof config?.siteName !== "string") return DEFAULT_SITE_NAME;
  return config.siteName.trim() || DEFAULT_SITE_NAME;
}

export function browserTitle(name: string, pageTitle: string): string {
  return `${name} - ${pageTitle}`;
}

export const siteName = readSiteName(window.HOOKFLY_CONFIG);
