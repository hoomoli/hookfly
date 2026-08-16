CREATE INDEX events_push_pipeline_correlation_idx
    ON events(provider, source_id, repository_id, revision, ref, received_at DESC, external_id, id DESC)
    WHERE event = 'pipeline'
      AND trigger = 'push'
      AND revision IS NOT NULL
      AND revision <> ''
      AND external_id IS NOT NULL
      AND external_id <> '';
