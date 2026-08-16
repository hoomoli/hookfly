CREATE TABLE hookfly_metadata (
    schema_version INTEGER NOT NULL CHECK (schema_version = 2)
);
INSERT INTO hookfly_metadata (schema_version) VALUES (2);

CREATE TABLE events (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider <> ''),
    source_id TEXT NOT NULL CHECK (source_id <> ''),
    repository_id TEXT NOT NULL,
    delivery_id TEXT NOT NULL CHECK (delivery_id <> ''),
    received_at INTEGER NOT NULL,
    event TEXT NOT NULL CHECK (event <> ''),
    ref TEXT,
    status TEXT,
    revision TEXT,
    external_id TEXT,
    trigger TEXT,
    routing_result TEXT NOT NULL,
    rule_id TEXT,
    rule_snapshot BLOB,
    config_digest TEXT,
    raw_headers_json BLOB,
    raw_payload_json BLOB,
    UNIQUE (provider, source_id, delivery_id)
);

CREATE TABLE deliveries (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    target_id TEXT NOT NULL,
    target_snapshot BLOB,
    current_attempt_id TEXT,
    transport_status TEXT NOT NULL DEFAULT 'pending',
    deployment_status TEXT NOT NULL DEFAULT 'not_started',
    created_at INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0,
    UNIQUE (event_id, target_id),
    FOREIGN KEY (id, current_attempt_id) REFERENCES delivery_attempts(delivery_id, id) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE delivery_attempts (
    id TEXT PRIMARY KEY,
    delivery_id TEXT NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    actor TEXT,
    transport_status TEXT NOT NULL DEFAULT 'pending',
    deployment_status TEXT NOT NULL DEFAULT 'not_started',
    request_json BLOB,
    response_json BLOB,
    request_at INTEGER,
    response_at INTEGER,
    enqueued_at INTEGER,
    deployment_id TEXT,
    poll_due_at INTEGER,
    monitoring_deadline_at INTEGER,
    last_polled_at INTEGER,
    created_at INTEGER NOT NULL,
    UNIQUE (delivery_id, id)
);

CREATE TABLE audit_logs (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    delivery_id TEXT REFERENCES deliveries(id) ON DELETE CASCADE,
    attempt_id TEXT REFERENCES delivery_attempts(id) ON DELETE CASCADE,
    actor TEXT,
    action TEXT NOT NULL,
    before_json BLOB,
    after_json BLOB,
    created_at INTEGER NOT NULL
);

CREATE TABLE routing_queue (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE REFERENCES events(id) ON DELETE CASCADE
);

CREATE INDEX events_received_at_idx ON events(received_at DESC);
CREATE INDEX events_rule_received_at_idx ON events(rule_id, received_at DESC);
CREATE INDEX events_deferred_recovery_idx ON events(id) WHERE routing_result = 'deferred';
CREATE INDEX deliveries_status_idx ON deliveries(transport_status, deployment_status);
CREATE INDEX deliveries_pending_work_idx ON deliveries(updated_at)
    WHERE transport_status IN ('pending', 'sending')
       OR deployment_status IN ('locating', 'running', 'unrecognized');
CREATE INDEX delivery_attempts_due_poll_idx ON delivery_attempts(poll_due_at)
    WHERE poll_due_at IS NOT NULL;
