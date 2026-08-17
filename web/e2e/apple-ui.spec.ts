import { expect, test, type Page } from "@playwright/test";

const idleStatus = {
  state: "idle",
  current_digest: "digest-browser",
  loaded_at: "2026-08-06T01:02:03Z",
  active_deliveries: 0,
  queued_events: 0,
};

const event = {
  id: "event-1",
  received_at: "2026-08-05T01:02:03Z",
  event_type: "pipeline",
  provider: "gitlab",
  source_id: "gitlab-primary",
  repository: "console",
  ref: "main",
  status: "success",
  revision: "0123456789abcdef",
  external_id: "42",
  trigger: "push",
  routing_result: "deploy",
  rule_id: "deploy-main",
  delivery_summary: { kind: "uniform", count: 1, transport_status: "enqueued", deployment_status: "done" },
  deliveries: [{
    id: "delivery-1",
    target_id: "target-1",
    transport_status: "succeeded",
    deployment_status: "done",
    active: false,
    attention: false,
    allowed_operations: [],
  }],
  active: false,
  attention: false,
};

const connection = { id: "dokploy-primary", type: "dokploy", status: "configured", capabilities: ["resource_discovery"] };
const connectionResource = {
  type: "compose", project_name: "Platform", environment_name: "Production", name: "Console",
  app_name: "console-abc123", resource_id: "compose-console", status: "done",
};
const targetInventory = {
  targets: [{
    id: "target-1", connection_id: "dokploy-primary", resource_type: "compose", resource_id: "compose-console",
    project_name: "Platform", environment_name: "Production", name: "Console", app_name: "console-abc123",
    status: "done", condition: "available", runtime_status: "all_running",
    containers: [{ name: "console-1", state: "running", health: "healthy", restart_count: 0 }],
  }],
  refreshed_at: "2026-08-11T10:00:00Z",
  errors: [],
};

const detail = {
  ...event,
  headers: { "content-type": "application/json" },
  payload: { safe: true },
  rule_snapshot: { id: "deploy-main" },
  activities: [],
  deliveries: [{
    id: "delivery-1",
    target_id: "target-1",
    current_attempt_id: "attempt-1",
    transport_status: "failed",
    deployment_status: "error",
    active: false,
    attention: true,
    target_binding_status: "compatible",
    allowed_operations: ["retry", "redeploy"],
    created_at: "2026-08-05T01:02:03Z",
    updated_at: "2026-08-05T01:03:03Z",
    attempts: [],
  }],
};

async function mockHookflyApi(page: Page, detailFails = false, ledgerEvent = event) {
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let body: unknown;
    if (path.endsWith("/auth/session")) body = { user: { display_name: "Browser Test", username: "browser-test", provider: "local" } };
    else if (path.endsWith("/notifications")) body = { items: [], latest_cursor: "" };
    else if (path.endsWith("/config/status")) body = idleStatus;
    else if (path.endsWith("/config/reload")) body = { status: "applied" };
    else if (path.endsWith("/repositories")) body = { repositories: [
      { provider: "gitlab", source_id: "gitlab-primary", id: "console", name: "Console" },
      { provider: "github", source_id: "github-primary", id: "worker", name: "Worker" },
    ] };
    else if (path.endsWith("/connections")) body = { connections: [connection] };
    else if (path.endsWith("/connections/dokploy-primary/resources")) body = { resources: [connectionResource] };
    else if (path.endsWith("/targets")) body = targetInventory;
    else if (path.endsWith("/events/event-1") && detailFails) return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: { code: "unavailable", message: "detail unavailable" } }) });
    else if (path.endsWith("/events/event-1")) body = detail;
    else if (path.endsWith("/events")) body = { items: [ledgerEvent], page: 1, page_size: 50, total: 1, has_previous: false, has_next: false, active: false };
    else return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: { code: "not_found", message: "not found" } }) });
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  });
}

test("keeps repository notification controls reachable in a short mobile dialog", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 480 });
  await mockHookflyApi(page);
  await page.goto("/");

  await page.getByRole("button", { name: "Settings center" }).click();
  const dialog = page.getByRole("dialog", { name: "Settings center" });
  await dialog.getByRole("button", { name: "Notifications" }).click();
  await dialog.getByRole("button", { name: "Repository Overrides" }).click();
  const lastControl = dialog.getByRole("checkbox", { name: "Deployment: Cancelled" });
  await lastControl.scrollIntoViewIfNeeded();
  await lastControl.focus();

  await expect(lastControl).toBeFocused();
  const bounds = await dialog.evaluate((element) => {
    const dialogRect = element.getBoundingClientRect();
    const focused = document.activeElement as HTMLElement;
    const focusedRect = focused.getBoundingClientRect();
    let scrollViewport: HTMLElement | null = focused.parentElement;
    while (scrollViewport && (getComputedStyle(scrollViewport).overflowY !== "auto" || scrollViewport.scrollHeight <= scrollViewport.clientHeight)) scrollViewport = scrollViewport.parentElement;
    const scrollRect = scrollViewport?.getBoundingClientRect();
    return {
      dialogTop: dialogRect.top,
      dialogBottom: dialogRect.bottom,
      focusedTop: focusedRect.top,
      focusedBottom: focusedRect.bottom,
      scrollTop: scrollRect?.top,
      scrollBottom: scrollRect?.bottom,
      scrollHeight: scrollViewport?.scrollHeight,
      scrollClientHeight: scrollViewport?.clientHeight,
      viewportHeight: window.innerHeight,
      pageScrollY: window.scrollY,
      pageWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
    };
  });
  expect(bounds.dialogTop).toBeGreaterThanOrEqual(0);
  expect(bounds.dialogBottom).toBeLessThanOrEqual(bounds.viewportHeight);
  expect(bounds.scrollTop).toBeDefined();
  expect(bounds.scrollHeight).toBeGreaterThan(bounds.scrollClientHeight!);
  expect(bounds.focusedTop).toBeGreaterThanOrEqual(bounds.scrollTop!);
  expect(bounds.focusedBottom).toBeLessThanOrEqual(bounds.scrollBottom!);
  expect(bounds.pageScrollY).toBe(0);
  expect(bounds.pageWidth).toBeLessThanOrEqual(bounds.viewportWidth);
});

const viewports = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "tablet", width: 768, height: 1024 },
  { name: "mobile", width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  for (const theme of ["light", "dark"] as const) {
    test(`${viewport.name} renders ${theme} without document overflow`, async ({ page }) => {
      await page.setViewportSize(viewport);
      await page.addInitScript((value) => localStorage.setItem("hookfly-appearance", value), theme);
      await mockHookflyApi(page);
      await page.goto("/");

      await expect(page.locator("html")).toHaveClass(new RegExp(theme));
      await expect(page.locator("html")).toHaveAttribute("lang", "en");
      await expect(page.getByRole("heading", { name: "Deployment activity ledger" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Choose theme" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Choose language" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Reload configuration" })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

      if (viewport.name === "mobile") {
        const controls = page.locator("button:not([disabled]), input, [role='combobox']");
        for (let index = 0; index < await controls.count(); index += 1) {
          const control = controls.nth(index);
          if (!await control.isVisible()) continue;
          const box = await control.boundingBox();
          expect(box, `mobile control ${index} has a box`).not.toBeNull();
          expect(Math.min(box!.width, box!.height), `mobile control ${index} is at least 44px`).toBeGreaterThanOrEqual(43.5);
        }
      }
    });
  }
}

test("aligns command actions and persists manual themes", async ({ page }) => {
  await mockHookflyApi(page);
  await page.goto("/");

  const reload = await page.getByRole("button", { name: "Reload configuration" }).boundingBox();
  const theme = await page.getByRole("button", { name: "Choose theme" }).boundingBox();
  expect(reload).not.toBeNull();
  expect(theme).not.toBeNull();
  expect(Math.abs((reload!.y + reload!.height / 2) - (theme!.y + theme!.height / 2))).toBeLessThanOrEqual(1);

  await page.getByRole("button", { name: "Choose theme" }).click();
  await page.getByRole("menuitemradio", { name: "Dark" }).click();
  await expect(page.locator("html")).toHaveClass(/dark/);
  await page.reload();
  await expect(page.locator("html")).toHaveClass(/dark/);
});

test("keeps every global action in one command row without an empty row above", async ({ page }) => {
  await page.setViewportSize({ width: 445, height: 900 });
  await page.addInitScript(() => localStorage.setItem("hookfly-language", "zh-CN"));
  await mockHookflyApi(page);
  await page.goto("/");

  const commandBar = page.locator('header:has([data-slot="command-actions"])');
  const controls = [
    page.getByRole("button", { name: "设置中心" }),
    page.getByRole("button", { name: "通知，0 条未读" }),
    page.getByRole("button", { name: "选择主题" }),
    page.getByRole("button", { name: "选择语言" }),
    page.getByRole("button", { name: "重新加载配置" }),
  ];
  const main = await page.locator("main").boundingBox();
  const commandBarBox = await commandBar.boundingBox();
  const boxes = await Promise.all(controls.map((control) => control.boundingBox()));

  expect(main).not.toBeNull();
  expect(commandBarBox).not.toBeNull();
  expect(Math.abs(commandBarBox!.y - main!.y)).toBeLessThanOrEqual(1);
  expect(boxes.every(Boolean)).toBe(true);
  const centers = boxes.map((box) => box!.y + box!.height / 2);
  expect(Math.max(...centers) - Math.min(...centers)).toBeLessThanOrEqual(1);
  expect(Math.min(...boxes.map((box) => box!.x))).toBeGreaterThanOrEqual(0);
  expect(Math.max(...boxes.map((box) => box!.x + box!.width))).toBeLessThanOrEqual(445);
});

test("keeps the unread notification badge inside the action scroller", async ({ page }) => {
  await page.setViewportSize({ width: 445, height: 900 });
  await page.addInitScript(() => localStorage.setItem("hookfly.notifications.v1", JSON.stringify({
    version: 2,
    initialized: true,
    cursor: "notification-1",
    items: [{
      id: "notification-1",
      cursor: "notification-1",
      category: "deployment",
      outcome: "failure",
      event_id: "event-1",
      target_id: "target-1",
      repository: "console",
      summary: "Deployment failed",
      occurred_at: "2026-08-13T00:00:00Z",
    }],
    unread_ids: ["notification-1"],
    preferences: {},
    system_enabled: false,
  })));
  await mockHookflyApi(page);
  await page.goto("/");

  const actions = page.locator('[data-slot="command-actions"]');
  const trigger = page.getByRole("button", { name: "Notifications, 1 unread" });
  const badge = trigger.locator("span");
  await expect(badge).toHaveText("1");

  const actionsBox = await actions.boundingBox();
  const badgeBox = await badge.boundingBox();
  expect(actionsBox).not.toBeNull();
  expect(badgeBox).not.toBeNull();
  expect(badgeBox!.x).toBeGreaterThanOrEqual(actionsBox!.x);
  expect(badgeBox!.y).toBeGreaterThanOrEqual(actionsBox!.y);
  expect(badgeBox!.x + badgeBox!.width).toBeLessThanOrEqual(actionsBox!.x + actionsBox!.width);
  expect(badgeBox!.y + badgeBox!.height).toBeLessThanOrEqual(actionsBox!.y + actionsBox!.height);
});

test("switches languages and persists the explicit choice", async ({ page }) => {
  await mockHookflyApi(page);
  await page.goto("/");

  await page.getByRole("button", { name: "Choose language" }).click();
  await page.getByRole("menuitemradio", { name: "简体中文" }).click();
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
  await expect(page.getByRole("heading", { name: "部署活动账本" })).toBeVisible();

  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
  await expect(page.getByRole("heading", { name: "部署活动账本" })).toBeVisible();
});

test("applies and persists one data font size across operational pages", async ({ page }) => {
  await mockHookflyApi(page);
  await page.goto("/");

  const ledgerData = page.locator("tbody.data-surface");
  await expect(ledgerData).toHaveCSS("font-size", "11px");
  await expect(ledgerData).not.toContainText("gitlab-primary");

  await page.getByRole("button", { name: "Settings center" }).click();
  await page.getByRole("radio", { name: "Comfortable 14 px" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-data-font-size", "comfortable");
  await expect(ledgerData).toHaveCSS("font-size", "14px");

  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-data-font-size", "comfortable");
  await expect(page.locator("tbody.data-surface")).toHaveCSS("font-size", "14px");

  await page.getByRole("button", { name: "Connections" }).click();
  await page.getByRole("button", { name: /dokploy-primary.*dokploy/i }).click();
  await expect(page.locator("tbody.data-surface")).toHaveCSS("font-size", "14px");

  await page.getByRole("button", { name: "Targets" }).click();
  await expect(page.locator("tbody.data-surface")).toHaveCSS("font-size", "14px");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("vertically centers every event cell when route targets wrap to two lines", async ({ page }) => {
  const secondDelivery = {
    ...event.deliveries[0],
    id: "delivery-2",
    target_id: "target-2",
  };
  const eventWithTwoTargets = {
    ...event,
    delivery_summary: { ...event.delivery_summary, count: 2 },
    deliveries: [...event.deliveries, secondDelivery],
  };
  await mockHookflyApi(page, false, eventWithTwoTargets);
  await page.goto("/");

  const row = page.locator("tbody.data-surface tr");
  await expect(row).toContainText("target-2");
  const alignment = await row.evaluate((element) => Array.from(element.querySelectorAll("td")).map((cell) => {
    const content = cell.firstElementChild;
    const cellRect = cell.getBoundingClientRect();
    const contentRect = content?.getBoundingClientRect();
    return {
      verticalAlign: getComputedStyle(cell).verticalAlign,
      centerDelta: contentRect ? Math.abs((cellRect.top + cellRect.height / 2) - (contentRect.top + contentRect.height / 2)) : Number.POSITIVE_INFINITY,
    };
  }));

  expect(alignment).toHaveLength(7);
  for (const cell of alignment) {
    expect(cell.verticalAlign).toBe("middle");
    expect(cell.centerDelta).toBeLessThanOrEqual(1);
  }
});

test("renders v2 repository identity and canonical event evidence", async ({ page }) => {
  await mockHookflyApi(page);
  await page.goto("/");

  await expect(page.getByRole("button", { name: /Console.*gitlab.*gitlab-primary/i })).toBeVisible();
  await page.getByRole("button", { name: "View event event-1" }).click();
  const inspector = page.getByRole("complementary", { name: "Event details" });
  await expect(inspector.getByRole("heading", { name: "console" })).toBeVisible();
  await expect(inspector.getByLabel("Event evidence")).toContainText("gitlab-primary");
  await expect(inspector.getByLabel("Event evidence")).toContainText("0123456789abcdef");
});

test("opens Connections and copies a Compose ID on mobile without overflow", async ({ page, context }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await mockHookflyApi(page);
  await page.goto("/");

  await page.getByRole("button", { name: "Connections" }).click();
  await expect(page.getByRole("heading", { name: "Connections" })).toBeVisible();
  await page.getByRole("button", { name: /dokploy-primary.*dokploy/i }).click();
  await expect(page.getByText("compose-console")).toBeVisible();
  await page.getByRole("button", { name: "Copy Compose ID compose-console" }).click();
  await expect(page.getByText("Copied")).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("compose-console");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("copies a Compose ID through the HTTP DOM fallback", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.addInitScript(() => {
    const clipboard = navigator.clipboard;
    Object.defineProperty(window, "__hookflyReadClipboard", { value: () => clipboard.readText() });
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
  });
  await mockHookflyApi(page);
  await page.goto("/");

  await page.getByRole("button", { name: "Connections" }).click();
  await page.getByRole("button", { name: /dokploy-primary.*dokploy/i }).click();
  await page.getByRole("button", { name: "Copy Compose ID compose-console" }).click();

  await expect(page.getByText("Copied")).toBeVisible();
  expect(await page.evaluate(() => (window as unknown as { __hookflyReadClipboard: () => Promise<string> }).__hookflyReadClipboard())).toBe("compose-console");
  expect(await page.locator("textarea[data-hookfly-clipboard]").count()).toBe(0);
  expect(await page.evaluate(() => document.getSelection()?.rangeCount ?? 0)).toBe(0);
});

test("supports keyboard dismissal for menu, Sheet, and Dialog", async ({ page }) => {
  await mockHookflyApi(page);
  await page.goto("/");

  const themeTrigger = page.getByRole("button", { name: "Choose theme" });
  await themeTrigger.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("menuitemradio", { name: "System" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(themeTrigger).toBeFocused();

  const eventTrigger = page.getByRole("button", { name: "View event event-1" });
  await eventTrigger.click();
  const inspector = page.getByRole("complementary", { name: "Event details" });
  await expect(inspector).toBeVisible();
  const retry = page.getByRole("button", { name: "Retry" });
  await retry.click();
  const dialog = page.getByRole("dialog", { name: "Confirm retry" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("textbox", { name: "Reason (optional)" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(retry).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(inspector).toBeHidden();
});

test("dismisses an inspector load failure with Escape", async ({ page }) => {
  await mockHookflyApi(page, true);
  await page.goto("/");
  await page.getByRole("button", { name: "View event event-1" }).click();
  const inspector = page.getByRole("complementary", { name: "Event details" });
  await expect(inspector).toContainText("detail unavailable");

  await page.keyboard.press("Escape");

  await expect(inspector).toBeHidden();
});

test("honors reduced motion for Sheet transitions", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await mockHookflyApi(page);
  await page.goto("/");
  await page.getByRole("button", { name: "View event event-1" }).click();
  const duration = await page.getByRole("complementary", { name: "Event details" }).evaluate((element) => getComputedStyle(element).animationDuration);
  const seconds = duration.endsWith("ms") ? Number.parseFloat(duration) / 1000 : Number.parseFloat(duration);
  expect(seconds).toBeLessThanOrEqual(0.001);
});
