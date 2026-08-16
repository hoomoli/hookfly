import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { HistoryClearDialog } from "./HistoryClearDialog";

function deferred() {
  let resolve!: () => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<void>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

async function openDialog(onConfirm: () => Promise<void>) {
  render(<HistoryClearDialog onConfirm={onConfirm} />);
  await userEvent.click(screen.getByRole("button", { name: "Clear all history" }));
  return screen.getByRole("dialog", { name: "Clear all event history?" });
}

it("opens a destructive confirmation that states the action cannot be undone", async () => {
  const dialog = await openDialog(vi.fn().mockResolvedValue(undefined));

  expect(within(dialog).getByText("This action cannot be undone.")).toBeInTheDocument();
  expect(within(dialog).getByRole("button", { name: "Clear all history" })).toHaveAttribute("data-variant", "destructive");
});

it("calls confirmation once and prevents dismissal while pending", async () => {
  const pending = deferred();
  const onConfirm = vi.fn(() => pending.promise);
  const dialog = await openDialog(onConfirm);

  await userEvent.click(within(dialog).getByRole("button", { name: "Clear all history" }));

  expect(onConfirm).toHaveBeenCalledOnce();
  expect(within(dialog).getByRole("button", { name: "Clearing history" })).toBeDisabled();
  expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeDisabled();
  await userEvent.keyboard("{Escape}");
  expect(screen.getByRole("dialog", { name: "Clear all event history?" })).toBeInTheDocument();

  pending.resolve();
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Clear all event history?" })).not.toBeInTheDocument());
  expect(onConfirm).toHaveBeenCalledOnce();
});

it("keeps the dialog open and renders a rejected error", async () => {
  const pending = deferred();
  const dialog = await openDialog(() => pending.promise);

  await userEvent.click(within(dialog).getByRole("button", { name: "Clear all history" }));
  pending.reject(new Error("History deletion failed"));

  expect(await within(dialog).findByRole("alert")).toHaveTextContent("History deletion failed");
  expect(screen.getByRole("dialog", { name: "Clear all event history?" })).toBeInTheDocument();
});
