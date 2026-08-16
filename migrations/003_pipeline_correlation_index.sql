CREATE INDEX events_pipeline_correlation_idx
    ON events(provider, source_id, repository_id, event, external_id, received_at DESC, id DESC)
    WHERE event = 'pipeline' AND external_id IS NOT NULL AND external_id <> '';
