ALTER TABLE import_checkpoints RENAME TO import_checkpoints_old;

CREATE TABLE import_checkpoints (
    source_id TEXT PRIMARY KEY,
    byte_offset INTEGER NOT NULL CHECK (byte_offset >= 0),
    record_sequence INTEGER NOT NULL CHECK (record_sequence >= -1),
    prefix_hash TEXT NOT NULL,
    last_record_hash TEXT NOT NULL,
    source_size INTEGER NOT NULL CHECK (source_size >= 0 AND byte_offset <= source_size),
    CHECK (record_sequence != -1 OR byte_offset = 0),
    CHECK (record_sequence != -1 OR last_record_hash = 'none'),
    CHECK (record_sequence = -1 OR last_record_hash != 'none')
) STRICT;

INSERT INTO import_checkpoints (
    source_id, byte_offset, record_sequence, prefix_hash, last_record_hash, source_size
)
SELECT source_id, byte_offset, record_sequence, prefix_hash, last_record_hash, source_size
FROM import_checkpoints_old;

DROP TABLE import_checkpoints_old;
