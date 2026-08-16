import { createInstance } from "i18next";
import { beforeEach, describe, expect, it } from "vitest";
import { initializeI18n, LANGUAGE_STORAGE_KEY } from "./i18n";

describe("initializeI18n", () => {
  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.lang = "";
  });

  it("uses English when no supported language was saved", () => {
    const instance = initializeI18n(createInstance(), window.localStorage);

    expect(instance.language).toBe("en");
    expect(document.documentElement.lang).toBe("en");
    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBeNull();
  });

  it("restores a saved Simplified Chinese choice", () => {
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, "zh-CN");

    const instance = initializeI18n(createInstance(), window.localStorage);

    expect(instance.language).toBe("zh-CN");
    expect(document.documentElement.lang).toBe("zh-CN");
  });

  it("composes the approved Simplified Chinese push outcome label", () => {
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, "zh-CN");

    const instance = initializeI18n(createInstance(), window.localStorage);

    expect(`${instance.t("settingsCenter.notifications.groups.push")}: ${instance.t("notifications.outcomes.received")}`).toBe("\u63a8\u9001: \u5df2\u6536\u5230");
  });

  it("distinguishes a GitLab resource wait from pipeline initialization", () => {
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, "zh-CN");

    const instance = initializeI18n(createInstance(), window.localStorage);

    expect(instance.t("sourceStatuses.waiting_for_resource")).toBe("\u7b49\u5f85\u8d44\u6e90");
  });

  it("persists supported language changes and updates the document", async () => {
    const instance = initializeI18n(createInstance(), window.localStorage);

    await instance.changeLanguage("zh-CN");

    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe("zh-CN");
    expect(document.documentElement.lang).toBe("zh-CN");
  });
});
