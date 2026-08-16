import { createInstance, type i18n as I18nInstance } from "i18next";
import { initReactI18next } from "react-i18next";
import { en } from "./locales/en";
import { zhCN } from "./locales/zh-CN";

export const LANGUAGE_STORAGE_KEY = "hookfly-language";
export const SUPPORTED_LANGUAGES = ["en", "zh-CN"] as const;
export type Language = (typeof SUPPORTED_LANGUAGES)[number];

function isLanguage(value: string | null): value is Language {
  return value !== null && SUPPORTED_LANGUAGES.some((language) => language === value);
}

function readLanguage(storage: Storage): Language {
  try {
    const stored = storage.getItem(LANGUAGE_STORAGE_KEY);
    return isLanguage(stored) ? stored : "en";
  } catch {
    return "en";
  }
}

function persistLanguage(storage: Storage, language: Language) {
  try {
    storage.setItem(LANGUAGE_STORAGE_KEY, language);
  } catch {
    // The selected language remains active when storage is unavailable.
  }
}

export function initializeI18n(instance: I18nInstance, storage: Storage): I18nInstance {
  const language = readLanguage(storage);
  void instance.use(initReactI18next).init({
    resources: { en, "zh-CN": zhCN },
    lng: language,
    fallbackLng: "en",
    supportedLngs: [...SUPPORTED_LANGUAGES],
    interpolation: { escapeValue: false },
    initAsync: false,
  });
  document.documentElement.lang = language;
  instance.on("languageChanged", (nextLanguage) => {
    if (!isLanguage(nextLanguage)) return;
    document.documentElement.lang = nextLanguage;
    persistLanguage(storage, nextLanguage);
  });
  return instance;
}

export const i18n = initializeI18n(createInstance(), window.localStorage);
