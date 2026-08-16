import { fireEvent, render, screen } from "@testing-library/react";
import { EventFilters } from "./EventFilters";

it("offers repository, target, delivery, and deployment filters without exact ref or rule inputs", () => {
  const onChange = vi.fn();
  render(<EventFilters
    filters={{ repository: "", target: "", transport: "", deployment: "" }}
    repositories={[{ provider: "gitlab", source_id: "gitlab-a", id: "app", name: "Application" }]}
    targets={[{ id: "production", connection_id: "primary", resource_type: "compose", resource_id: "compose-1", condition: "available", containers: [] }]}
    onChange={onChange}
    onClear={vi.fn()}
  />);

  expect(screen.queryByLabelText("Ref")).toBeNull();
  expect(screen.queryByLabelText("Rule")).toBeNull();
  fireEvent.click(screen.getByLabelText("Repository"));
  fireEvent.click(screen.getByRole("option", { name: /Application.*GitLab/i }));
  expect(onChange).toHaveBeenCalledWith("repository", "gitlab-a\u0000app");
  fireEvent.click(screen.getByLabelText("Target"));
  fireEvent.click(screen.getByRole("option", { name: "production" }));
  expect(onChange).toHaveBeenCalledWith("target", "production");
  expect(screen.getByLabelText("Delivery status")).toBeInTheDocument();
  expect(screen.getByLabelText("Deployment status")).toBeInTheDocument();
});
