import type {
  AuthSession,
  ApiErrorBody,
  ConnectionResource,
  ConnectionSummary,
  ConfigReloadResponse,
  ClearHistoryResponse,
  ConfigStatus,
  EventDetailResponse,
  EventPage,
  ManualAttemptRequest,
  ManualAttemptResponse,
  Repository,
  TargetInventory,
  NotificationFeed,
} from "./types";
import { i18n } from "./i18n";

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, init);
  const body = await response.json() as T | ApiErrorBody;
  if (!response.ok) {
    const error = body as ApiErrorBody;
    throw new ApiError(response.status, error.error?.code ?? "unavailable", error.error?.message ?? i18n.t("errors.requestFailed"));
  }
  return body as T;
}

export function getAuthSession(): Promise<AuthSession> {
  return request<AuthSession>("/api/v1/auth/session");
}

export async function logout(): Promise<void> {
  const response = await fetch("/api/v1/auth/logout", { method: "POST" });
  if (!response.ok) {
    let error: ApiErrorBody | undefined;
    try {
      error = await response.json() as ApiErrorBody;
    } catch {
      // The stable fallback keeps upstream or proxy error pages out of the UI.
    }
    throw new ApiError(response.status, error?.error?.code ?? "unavailable", error?.error?.message ?? i18n.t("errors.requestFailed"));
  }
}

export async function listRepositories(): Promise<Repository[]> {
  const response = await request<{ repositories: Repository[] }>("/api/v1/repositories");
  return response.repositories;
}

export function listNotifications(after?: string): Promise<NotificationFeed> {
  const query = new URLSearchParams({ limit: "100" });
  if (after) query.set("after", after);
  return request<NotificationFeed>(`/api/v1/notifications?${query.toString()}`);
}

export function openNotificationStream(after?: string): EventSource {
  const query = new URLSearchParams();
  if (after) query.set("after", after);
  const suffix = query.size ? `?${query.toString()}` : "";
  return new EventSource(`/api/v1/notifications/stream${suffix}`);
}

export async function listConnections(): Promise<ConnectionSummary[]> {
  const response = await request<{ connections: ConnectionSummary[] }>("/api/v1/connections");
  return response.connections;
}

export async function listConnectionResources(connectionID: string, signal?: AbortSignal): Promise<ConnectionResource[]> {
  const response = await request<{ resources: ConnectionResource[] }>(
    `/api/v1/connections/${encodeURIComponent(connectionID)}/resources`,
    { signal },
  );
  return response.resources;
}

export function listTargets(): Promise<TargetInventory> {
  return request<TargetInventory>("/api/v1/targets");
}

export function getConfigStatus(): Promise<ConfigStatus> {
  return request<ConfigStatus>("/api/v1/config/status");
}

export function reloadConfig(): Promise<ConfigReloadResponse> {
  return request<ConfigReloadResponse>("/api/v1/config/reload", { method: "POST" });
}

export function listEvents(query: URLSearchParams): Promise<EventPage> {
  const suffix = query.size > 0 ? `?${query.toString()}` : "";
  return request<EventPage>(`/api/v1/events${suffix}`);
}

export function clearHistory(): Promise<ClearHistoryResponse> {
  return request<ClearHistoryResponse>("/api/v1/events", { method: "DELETE" });
}

export function getEvent(eventID: string): Promise<EventDetailResponse> {
  return request<EventDetailResponse>(`/api/v1/events/${encodeURIComponent(eventID)}`);
}

export function createAttempt(deliveryID: string, body: ManualAttemptRequest): Promise<ManualAttemptResponse> {
  return request<ManualAttemptResponse>(`/api/v1/deliveries/${encodeURIComponent(deliveryID)}/attempts`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}
