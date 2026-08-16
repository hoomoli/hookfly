export type JsonValue = null | boolean | number | string | JsonValue[] | { [key: string]: JsonValue };
export type Operation = "retry" | "redeploy";
export type TargetBindingStatus = "compatible" | "target_changed" | "target_unavailable";
export type ConfigReloadState = "idle" | "validating" | "waiting" | "applying" | "draining";
export type NotificationCategory = "push" | "pipeline" | "deployment";
export type PushNotificationOutcome = "received";
export type PipelineNotificationOutcome = "initializing" | "waiting" | "running" | "success" | "failure" | "cancelled";
export type DeploymentNotificationOutcome = "pending" | "triggering" | "running" | "success" | "failure" | "timeout" | "cancelled";
export type NotificationOutcome = PushNotificationOutcome | PipelineNotificationOutcome | DeploymentNotificationOutcome;
export type NotificationStatusKey = `push.${PushNotificationOutcome}` | `pipeline.${PipelineNotificationOutcome}` | `deployment.${DeploymentNotificationOutcome}`;

export interface NotificationFact {
  id: string;
  cursor: string;
  category: NotificationCategory;
  outcome: NotificationOutcome;
  event_id: string;
  delivery_id?: string;
  target_id?: string;
  provider?: "gitlab" | "github";
  source_id?: string;
  repository: string;
  summary: string;
  occurred_at: string;
}

export interface NotificationFeed { items: NotificationFact[]; latest_cursor: string }
export type NotificationPreferences = Record<NotificationStatusKey, boolean>;

export interface StoredNotificationState {
  version: 3;
  initialized: boolean;
  cursor: string;
  items: NotificationFact[];
  unread_ids: string[];
  preferences: NotificationPreferences;
  repository_preferences: Record<string, NotificationPreferences>;
  system_enabled: boolean;
}

export interface ConfigStatus {
  state: ConfigReloadState;
  current_digest: string;
  loaded_at: string;
  pending_digest?: string;
  recovery?: boolean;
  active_deliveries: number;
  queued_events: number;
  last_result?: { code: string; at: string };
}

export interface ConfigReloadResponse {
  status: "applied" | "waiting";
}

export interface ClearHistoryResponse {
  deleted: number;
}

export interface Repository {
  provider: "gitlab" | "github";
  source_id: string;
  id: string;
  name: string;
}

export interface ConnectionSummary {
  id: string;
  type: string;
  status: string;
  capabilities: string[];
}

export interface ConnectionResource {
  type: string;
  project_name: string;
  environment_name: string;
  name: string;
  app_name?: string;
  resource_id: string;
  status?: string;
}

export type TargetCondition = "available" | "unavailable" | "refresh_failed";
export type TargetRuntimeStatus = "all_running" | "degraded" | "stopped" | "empty" | "unknown";

export interface TargetContainer {
  name: string;
  state: string;
  health?: string;
  restart_count: number;
}

export interface TargetInventoryItem {
  id: string;
  connection_id: string;
  resource_type: string;
  resource_id: string;
  project_name?: string;
  environment_name?: string;
  name?: string;
  app_name?: string;
  status?: string;
  condition: TargetCondition;
  runtime_status?: TargetRuntimeStatus;
  containers: TargetContainer[];
}

export interface TargetInventoryError {
  connection_id: string;
  code: string;
}

export interface TargetInventory {
  targets: TargetInventoryItem[];
  refreshed_at: string;
  errors: TargetInventoryError[];
}

export type RepositorySelection =
  | { kind: "all" }
  | { kind: "unmatched" }
  | { kind: "repository"; sourceID: string; repositoryID: string };

export interface DeliverySummary {
  kind: "none" | "uniform" | "mixed";
  count: number;
  transport_status: string | null;
  deployment_status: string | null;
}

export interface EventDeliverySummary {
  id: string;
  target_id: string;
  current_attempt_id: string;
  transport_status: string;
  deployment_status: string;
  active: boolean;
  attention: boolean;
  allowed_operations: Operation[];
}

export interface EventSummary {
  id: string;
  received_at: string;
  event_type: string;
  provider: "gitlab" | "github";
  source_id: string;
  repository: string;
  ref: string | null;
  status: string | null;
  revision: string | null;
  commit_message: string | null;
  external_id: string | null;
  trigger: string | null;
  routing_result: string;
  rule_id: string | null;
  delivery_summary: DeliverySummary;
  deliveries: EventDeliverySummary[];
  active: boolean;
  attention: boolean;
}

export interface EventPage {
  items: EventSummary[];
  page: number;
  page_size: number;
  total: number;
  has_previous: boolean;
  has_next: boolean;
  active: boolean;
}

export interface Attempt {
  id: string;
  kind: string;
  actor: string | null;
  current: boolean;
  transport_status: string;
  deployment_status: string;
  deployment_id: string | null;
  request_snapshot: JsonValue;
  response_snapshot: JsonValue;
  deploy_command: string | null;
  request_at: string | null;
  response_at: string | null;
  enqueued_at: string | null;
  monitoring_deadline_at: string | null;
  created_at: string;
}

export interface Delivery {
  id: string;
  target_id: string;
  current_attempt_id: string;
  transport_status: string;
  deployment_status: string;
  active: boolean;
  attention: boolean;
  target_binding_status: TargetBindingStatus;
  allowed_operations: Operation[];
  created_at: string;
  updated_at: string;
  attempts: Attempt[];
}

export interface Activity {
  id: string;
  delivery_id: string | null;
  attempt_id: string | null;
  actor: string | null;
  action: string;
  before: JsonValue;
  after: JsonValue;
  created_at: string;
}

export interface EventDetailResponse extends EventSummary {
  headers: JsonValue;
  payload: JsonValue;
  rule_snapshot: JsonValue;
  source_states?: Array<{ event_id: string; status: string; received_at: string }>;
  deliveries: Delivery[];
  activities: Activity[];
}

export interface ApiErrorBody {
  error: { code: string; message: string };
}

export interface ManualAttemptRequest {
  expected_current_attempt_id: string;
  operation: Operation;
  reason: string;
}

export interface ManualAttemptResponse {
  delivery_id: string;
  attempt_id: string;
}
export interface AuthSession {
  user: {
    display_name: string;
    username?: string;
    provider: "authentik" | "local";
  };
  expires_at?: string;
}
