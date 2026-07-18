ALTER TABLE raw_records
ADD COLUMN retention_policy_version INTEGER NOT NULL DEFAULT 1
CHECK (retention_policy_version > 0);

ALTER TABLE events
ADD COLUMN retention_policy_version INTEGER NOT NULL DEFAULT 1
CHECK (retention_policy_version > 0);

ALTER TABLE events
ADD COLUMN payload_storage TEXT NOT NULL DEFAULT 'inline'
CHECK (payload_storage IN ('inline', 'detached'));

CREATE TABLE event_payloads (
    event_id TEXT PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    retention_policy_version INTEGER NOT NULL CHECK (retention_policy_version > 0),
    storage_encoding TEXT NOT NULL CHECK (storage_encoding = 'zlib'),
    original_size INTEGER NOT NULL CHECK (original_size > 262144),
    content BLOB NOT NULL
) STRICT;
