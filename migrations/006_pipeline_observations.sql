CREATE TABLE pipeline_observations (
    event_id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    source_id TEXT NOT NULL,
    repository_id TEXT NOT NULL,
    external_id TEXT NOT NULL,
    ref TEXT NOT NULL,
    status TEXT NOT NULL,
    received_at INTEGER NOT NULL
);
CREATE INDEX pipeline_observations_time ON pipeline_observations(received_at);
CREATE INDEX pipeline_observations_identity ON pipeline_observations(provider, source_id, repository_id, external_id, received_at);
INSERT INTO pipeline_observations
SELECT id, provider, source_id, repository_id, external_id, COALESCE(ref, ''), COALESCE(status, ''), received_at
FROM events WHERE event = 'pipeline' AND external_id IS NOT NULL AND external_id <> '';
CREATE TRIGGER record_pipeline_observation AFTER INSERT ON events
WHEN NEW.event = 'pipeline' AND NEW.external_id IS NOT NULL AND NEW.external_id <> ''
BEGIN
    INSERT OR IGNORE INTO pipeline_observations VALUES
    (NEW.id, NEW.provider, NEW.source_id, NEW.repository_id, NEW.external_id, COALESCE(NEW.ref, ''), COALESCE(NEW.status, ''), NEW.received_at);
END;
