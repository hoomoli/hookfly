import { createRef } from "react";
import { render, screen } from "@testing-library/react";
import { Button } from "./button";

it("forwards native semantics, variants, caller classes, and refs", () => {
  const ref = createRef<HTMLButtonElement>();
  render(<Button ref={ref} disabled variant="primary" className="command-action">Reload configuration</Button>);

  const button = screen.getByRole("button", { name: "Reload configuration" });
  expect(button).toBeDisabled();
  expect(button).toHaveClass("command-action");
  expect(button).toHaveAttribute("data-slot", "button");
  expect(button).toHaveAttribute("data-variant", "primary");
  expect(ref.current).toBe(button);
});
