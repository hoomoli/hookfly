import { render, screen } from "@testing-library/react";
import { ProviderBadge } from "./ProviderBadge";

it("renders Harbor explicitly instead of falling back to GitLab", () => {
  render(<ProviderBadge provider="harbor" />);

  expect(screen.getByText("Harbor")).toBeInTheDocument();
  expect(screen.queryByText("GitLab")).toBeNull();
});
