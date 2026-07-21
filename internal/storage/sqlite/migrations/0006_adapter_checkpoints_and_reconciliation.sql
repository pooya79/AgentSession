ALTER TABLE import_checkpoints RENAME TO import_checkpoints_old;

CREATE TABLE import_checkpoints (
    source_id TEXT PRIMARY KEY,
    record_sequence INTEGER NOT NULL CHECK (record_sequence >= -1),
    state_version TEXT NOT NULL CHECK (length(trim(state_version)) > 0),
    cursor BLOB NOT NULL CHECK (length(cursor) > 0),
    fingerprint BLOB NOT NULL CHECK (length(fingerprint) > 0)
) STRICT;

INSERT INTO import_checkpoints (source_id, record_sequence, state_version, cursor, fingerprint)
SELECT
    source_id,
    record_sequence,
    'legacy-stream-v1',
    CAST('offset=' || byte_offset || ';size=' || source_size AS BLOB),
    CAST(prefix_hash || char(0) || last_record_hash AS BLOB)
FROM import_checkpoints_old;

DROP TABLE import_checkpoints_old;

CREATE TABLE reconciliation_runs (
    run_id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL UNIQUE,
    expected_record_sequence INTEGER NOT NULL CHECK (expected_record_sequence >= -1),
    expected_state_version TEXT NOT NULL,
    expected_cursor BLOB NOT NULL,
    expected_fingerprint BLOB NOT NULL
) STRICT;

CREATE TABLE reconciliation_batches (
    run_id TEXT NOT NULL REFERENCES reconciliation_runs(run_id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    batch BLOB NOT NULL,
    PRIMARY KEY (run_id, ordinal)
) STRICT;
