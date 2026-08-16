ALTER TABLE deliveries ADD COLUMN dispatch_key TEXT;
ALTER TABLE delivery_attempts ADD COLUMN deployment_cursor TEXT;

CREATE INDEX deliveries_dispatch_work_idx ON deliveries(dispatch_key)
    WHERE dispatch_key IS NOT NULL;
