import { browserTitle, readSiteName } from "./site-config";

it("uses a configured site name", () => {
  // Break caught: the runtime configuration is present but the UI continues to show Hookfly.
  expect(readSiteName({ siteName: "Deployment Hub" })).toBe("Deployment Hub");
});

it("falls back to Hookfly when the configured site name is blank", () => {
  // Break caught: an unset deployment variable leaves visible branding blank.
  expect(readSiteName({ siteName: "   " })).toBe("Hookfly");
});

it("uses the configured site name in the browser title", () => {
  // Break caught: browser tabs continue to display Hookfly after a deployment changes the UI brand.
  expect(browserTitle("Deployment Hub", "Deployment ledger")).toBe("Deployment Hub - Deployment ledger");
});
