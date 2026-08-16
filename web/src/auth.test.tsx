import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthGate } from "./auth";

const session = {
  user: { display_name: "dj", username: "dj", provider: "authentik" as const },
  expires_at: "2026-08-12T16:00:00Z",
};

it("does not mount protected children before the session resolves", async () => {
  // Break caught: anonymous startup fires business API hooks before authentication is established.
  let resolveSession: ((response: Response) => void) | undefined;
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    requests.push(String(input));
    return new Promise<Response>((resolve) => { resolveSession = resolve; });
  }));

  render(<AuthGate>{() => <div>Protected management UI</div>}</AuthGate>);

  expect(screen.queryByText("Protected management UI")).not.toBeInTheDocument();
  expect(requests).toEqual(["/api/v1/auth/session"]);
  resolveSession?.(new Response(JSON.stringify(session), { status: 200 }));
  expect(await screen.findByText("Protected management UI")).toBeInTheDocument();
});

it("sends anonymous users to login with the current local return path", async () => {
  window.history.replaceState(null, "", "/?view=targets");
  let destination = "";
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { code: "unauthorized", message: "authentication required" } }), { status: 401 })));

  render(<AuthGate navigate={(url) => { destination = url; }}>{() => <div>Protected management UI</div>}</AuthGate>);

  await waitFor(() => expect(destination).toBe("/api/v1/auth/login?return_to=%2F%3Fview%3Dtargets"));
  expect(screen.queryByText("Protected management UI")).not.toBeInTheDocument();
});

it("renders access denied without mounting protected children", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { code: "forbidden", message: "permission denied" } }), { status: 403 })));

  render(<AuthGate>{() => <div>Protected management UI</div>}</AuthGate>);

  expect(await screen.findByText("Access denied")).toBeInTheDocument();
  expect(screen.queryByText("Protected management UI")).not.toBeInTheDocument();
});

it("renders an unavailable state and retries session resolution", async () => {
  let calls = 0;
  vi.stubGlobal("fetch", vi.fn(async () => {
    calls += 1;
    if (calls === 1) return new Response(JSON.stringify({ error: { code: "unavailable", message: "service unavailable" } }), { status: 503 });
    return new Response(JSON.stringify(session), { status: 200 });
  }));

  render(<AuthGate>{() => <div>Protected management UI</div>}</AuthGate>);

  expect(await screen.findByText("Authentication unavailable")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("Protected management UI")).toBeInTheDocument();
});

it("keeps the signed-out view after local logout until sign in is explicit", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).endsWith("/logout")) return new Response(null, { status: 204 });
    return new Response(JSON.stringify(session), { status: 200 });
  }));

  render(<AuthGate>{(_session, signOut) => <button onClick={() => void signOut()}>Account sign out</button>}</AuthGate>);
  await userEvent.click(await screen.findByRole("button", { name: "Account sign out" }));

  expect(await screen.findByText("Signed out")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
});

it("shows a retryable error when logout fails", async () => {
  let logoutCalls = 0;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).endsWith("/logout")) {
      logoutCalls += 1;
      return logoutCalls === 1
        ? new Response(JSON.stringify({ error: { code: "unavailable", message: "service unavailable" } }), { status: 503 })
        : new Response(null, { status: 204 });
    }
    return new Response(JSON.stringify(session), { status: 200 });
  }));

  render(<AuthGate>{(_session, signOut) => <button onClick={() => void signOut()}>Account sign out</button>}</AuthGate>);
  await userEvent.click(await screen.findByRole("button", { name: "Account sign out" }));

  expect(await screen.findByText("Sign out failed")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("Signed out")).toBeInTheDocument();
  expect(logoutCalls).toBe(2);
});
