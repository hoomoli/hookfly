import { cn } from "./utils";

it("lets the caller override a Tailwind utility", () => {
  expect(cn("px-2 text-sm", "px-4")).toBe("text-sm px-4");
});
