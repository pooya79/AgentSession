CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    started_at TEXT,
    ended_at TEXT,
    source_id TEXT NOT NULL,
    adapter_name TEXT NOT NULL,
    adapter_version TEXT NOT NULL,
    format_version TEXT NOT NULL,
    model_version TEXT NOT NULL,
    normalization_version TEXT NOT NULL
) STRICT;

CREATE TABLE raw_records (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL,
    record_sequence INTEGER CHECK (record_sequence IS NULL OR record_sequence >= 0),
    byte_offset INTEGER CHECK (byte_offset IS NULL OR byte_offset >= 0),
    byte_length INTEGER CHECK (byte_length IS NULL OR byte_length > 0),
    content_hash TEXT NOT NULL,
    storage_encoding TEXT NOT NULL CHECK (storage_encoding IN ('identity', 'zlib')),
    original_size INTEGER NOT NULL CHECK (original_size >= 0),
    content BLOB NOT NULL,
    CHECK ((byte_offset IS NULL) = (byte_length IS NULL))
) STRICT;

CREATE INDEX raw_records_session ON raw_records(session_id);

CREATE TABLE events (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL CHECK (sequence >= 0),
    timestamp TEXT,
    kind TEXT NOT NULL,
    summary TEXT NOT NULL,
    searchable_text TEXT NOT NULL,
    data_json TEXT NOT NULL,
    raw_record_id TEXT NOT NULL REFERENCES raw_records(id),
    raw_source_id TEXT NOT NULL,
    raw_record_sequence INTEGER CHECK (raw_record_sequence IS NULL OR raw_record_sequence >= 0),
    raw_byte_offset INTEGER CHECK (raw_byte_offset IS NULL OR raw_byte_offset >= 0),
    raw_byte_length INTEGER CHECK (raw_byte_length IS NULL OR raw_byte_length > 0),
    raw_content_hash TEXT NOT NULL,
    UNIQUE (session_id, sequence),
    CHECK ((raw_byte_offset IS NULL) = (raw_byte_length IS NULL))
) STRICT;

CREATE INDEX events_session_order ON events(session_id, sequence);

CREATE TABLE session_diagnostics (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    code TEXT NOT NULL,
    severity TEXT NOT NULL,
    message TEXT NOT NULL,
    event_ids_json TEXT NOT NULL,
    raw_record_ids_json TEXT NOT NULL,
    PRIMARY KEY (session_id, position)
) STRICT;

CREATE TABLE import_checkpoints (
    source_id TEXT PRIMARY KEY,
    byte_offset INTEGER NOT NULL CHECK (byte_offset >= 0),
    record_sequence INTEGER NOT NULL CHECK (record_sequence >= 0),
    prefix_hash TEXT NOT NULL,
    last_record_hash TEXT NOT NULL,
    source_size INTEGER NOT NULL CHECK (source_size >= 0 AND byte_offset <= source_size)
) STRICT;
