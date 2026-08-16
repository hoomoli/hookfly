import { fireEvent, render, screen } from "@testing-library/react";
import { i18n } from "../i18n";
import type { Attempt } from "../types";
import { AttemptTimeline } from "./AttemptTimeline";

const originalClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
const originalExecCommand = Object.getOwnPropertyDescriptor(document, "execCommand");

const command = "curl --request POST \\\n  --url 'https://dokploy.example.invalid/api/compose.deploy' \\\n  --header 'Content-Type: application/json' \\\n  --header \"x-api-key: ${DOKPLOY_API_KEY}\" \\\n  --data '{\"composeId\":\"compose-1\"}'";

function attempt(deployCommand: string | null): Attempt {
  return {
    id: "attempt-1",
    kind: "initial",
    actor: null,
    current: true,
    transport_status: "enqueued",
    deployment_status: "running",
    deployment_id: null,
    request_snapshot: null,
    response_snapshot: null,
    deploy_command: deployCommand,
    request_at: "2026-08-13T01:00:00Z",
    response_at: null,
    enqueued_at: "2026-08-13T01:00:01Z",
    monitoring_deadline_at: null,
    created_at: "2026-08-13T01:00:00Z",
  };
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(() => {
  if (originalClipboard) Object.defineProperty(navigator, "clipboard", originalClipboard);
  else Reflect.deleteProperty(navigator, "clipboard");
  if (originalExecCommand) Object.defineProperty(document, "execCommand", originalExecCommand);
  else Reflect.deleteProperty(document, "execCommand");
});

it("renders deployment curl only for attempts with recorded POST evidence", () => {
  const { rerender } = render(<AttemptTimeline attempts={[attempt(null)]} />);
  expect(screen.queryByText("Deployment request")).toBeNull();

  rerender(<AttemptTimeline attempts={[attempt(command)]} />);
  fireEvent.click(screen.getByText("Deployment request"));
  expect(document.querySelector("pre")?.textContent).toBe(command);
  expect(screen.getByText("The API key is a placeholder and was not stored.")).toBeInTheDocument();
  expect(screen.getByText("This records Hookfly's send attempt; use the transport status to determine whether Dokploy may have received it.")).toBeInTheDocument();
});

it("copies the exact recorded deployment curl", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  render(<AttemptTimeline attempts={[attempt(command)]} />);

  fireEvent.click(screen.getByRole("button", { name: "Copy deployment request" }));

  expect(writeText).toHaveBeenCalledWith(command);
  expect(await screen.findByRole("button", { name: "Deployment request copied" })).toBeInTheDocument();
});

it.each([
  ["without Clipboard API", undefined],
  ["after Clipboard API rejection", { writeText: vi.fn().mockRejectedValue(new Error("denied with browser details")) }],
])("copies through the DOM fallback %s", async (_name, clipboard) => {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: clipboard });
  const execCommand = vi.fn(() => {
    expect(document.querySelector<HTMLTextAreaElement>("textarea[data-hookfly-deploy-clipboard]")?.value).toBe(command);
    return true;
  });
  Object.defineProperty(document, "execCommand", { configurable: true, value: execCommand });
  render(<AttemptTimeline attempts={[attempt(command)]} />);

  fireEvent.click(screen.getByRole("button", { name: "Copy deployment request" }));

  expect(await screen.findByRole("button", { name: "Deployment request copied" })).toBeInTheDocument();
  expect(execCommand).toHaveBeenCalledWith("copy");
  expect(document.querySelector("textarea[data-hookfly-deploy-clipboard]")).toBeNull();
});

it("reports a copy failure without exposing browser error details", async () => {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
  Object.defineProperty(document, "execCommand", { configurable: true, value: vi.fn(() => false) });
  render(<AttemptTimeline attempts={[attempt(command)]} />);

  fireEvent.click(screen.getByRole("button", { name: "Copy deployment request" }));

  expect(await screen.findByRole("alert")).toHaveTextContent("Could not copy the deployment request. Select and copy it manually.");
  expect(document.querySelector("textarea[data-hookfly-deploy-clipboard]")).toBeNull();
});
