CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
) STRICT;

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
    normalization_version TEXT NOT NULL,
    canonical_revision INTEGER NOT NULL DEFAULT 0 CHECK (canonical_revision >= 0),
    last_activity_at TEXT,
    first_user_message TEXT NOT NULL DEFAULT '',
    event_count INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0)
) STRICT;

CREATE INDEX sessions_exploration_order ON sessions(started_at DESC, id ASC);
CREATE INDEX sessions_last_activity_order ON sessions(last_activity_at DESC, id ASC);

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
    retention_policy_version INTEGER NOT NULL DEFAULT 1 CHECK (retention_policy_version > 0),
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
    retention_policy_version INTEGER NOT NULL DEFAULT 1 CHECK (retention_policy_version > 0),
    payload_storage TEXT NOT NULL DEFAULT 'inline' CHECK (payload_storage IN ('inline', 'detached')),
    message_role TEXT NOT NULL DEFAULT '',
    UNIQUE (session_id, sequence),
    CHECK ((raw_byte_offset IS NULL) = (raw_byte_length IS NULL))
) STRICT;

CREATE INDEX events_session_order ON events(session_id, sequence);
CREATE INDEX events_session_timestamp ON events(session_id, timestamp);

CREATE TABLE event_payloads (
    event_id TEXT PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    retention_policy_version INTEGER NOT NULL CHECK (retention_policy_version > 0),
    storage_encoding TEXT NOT NULL CHECK (storage_encoding = 'zlib'),
    original_size INTEGER NOT NULL CHECK (original_size > 262144),
    content BLOB NOT NULL
) STRICT;

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

CREATE TABLE record_diagnostics (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    raw_record_id TEXT NOT NULL REFERENCES raw_records(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    code TEXT NOT NULL,
    severity TEXT NOT NULL,
    message TEXT NOT NULL,
    interpretation_reason TEXT NOT NULL DEFAULT ''
        CHECK (interpretation_reason IN ('', 'missing_discriminant', 'structurally_invalid_known_record')),
    event_ids_json TEXT NOT NULL,
    raw_record_ids_json TEXT NOT NULL,
    PRIMARY KEY (raw_record_id, ordinal)
) STRICT;

CREATE INDEX record_diagnostics_session ON record_diagnostics(session_id);
CREATE INDEX record_diagnostics_interpretation_coverage
    ON record_diagnostics(session_id, interpretation_reason, raw_record_id);

CREATE TABLE import_checkpoints (
    source_id TEXT PRIMARY KEY,
    record_sequence INTEGER NOT NULL CHECK (record_sequence >= -1),
    state_version TEXT NOT NULL CHECK (length(trim(state_version)) > 0),
    cursor BLOB NOT NULL CHECK (length(cursor) > 0),
    fingerprint BLOB NOT NULL CHECK (length(fingerprint) > 0)
) STRICT;

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

CREATE TABLE container_memberships (
    container_source_id TEXT NOT NULL,
    child_source_id TEXT NOT NULL UNIQUE,
    PRIMARY KEY (container_source_id, child_source_id)
) STRICT;

CREATE TABLE projection_definitions (
    kind TEXT PRIMARY KEY CHECK (kind IN ('search', 'git_correlation', 'findings', 'outcomes', 'aggregates')),
    target_version TEXT NOT NULL CHECK (length(trim(target_version)) > 0),
    updated_at TEXT NOT NULL
) STRICT;

INSERT INTO projection_definitions (kind, target_version, updated_at) VALUES
    ('search', '1', CURRENT_TIMESTAMP),
    ('git_correlation', '1', CURRENT_TIMESTAMP),
    ('findings', '1', CURRENT_TIMESTAMP),
    ('outcomes', '1', CURRENT_TIMESTAMP),
    ('aggregates', '1', CURRENT_TIMESTAMP);

CREATE TABLE session_projection_states (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    kind TEXT NOT NULL REFERENCES projection_definitions(kind),
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'failed', 'ready')),
    target_version TEXT NOT NULL,
    target_revision INTEGER NOT NULL CHECK (target_revision >= 0),
    ready_version TEXT,
    ready_revision INTEGER CHECK (ready_revision IS NULL OR ready_revision >= 0),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    run_token TEXT,
    started_at TEXT,
    lease_expires_at TEXT,
    updated_at TEXT NOT NULL,
    failure_code TEXT,
    failure_summary TEXT,
    failure_attempt INTEGER CHECK (failure_attempt IS NULL OR failure_attempt > 0),
    failure_at TEXT,
    PRIMARY KEY (session_id, kind),
    CHECK ((ready_version IS NULL) = (ready_revision IS NULL)),
    CHECK ((status = 'running') = (run_token IS NOT NULL)),
    CHECK ((status = 'running') = (lease_expires_at IS NOT NULL))
) STRICT;

CREATE INDEX session_projection_work ON session_projection_states(status, session_id, kind);

CREATE TABLE search_document_stage (
    build_token TEXT NOT NULL,
    session_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence >= 0),
    timestamp TEXT,
    kind TEXT NOT NULL,
    summary TEXT NOT NULL,
    content TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    command_text TEXT NOT NULL,
    projection_version TEXT NOT NULL,
    canonical_revision INTEGER NOT NULL CHECK (canonical_revision >= 0),
    PRIMARY KEY (build_token, event_id)
) STRICT;

CREATE TABLE search_file_stage (
    build_token TEXT NOT NULL,
    event_id TEXT NOT NULL,
    path TEXT NOT NULL,
    PRIMARY KEY (build_token, event_id, path),
    FOREIGN KEY (build_token, event_id)
        REFERENCES search_document_stage(build_token, event_id) ON DELETE CASCADE
) STRICT;

CREATE TABLE search_documents (
    rowid INTEGER PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    event_id TEXT NOT NULL UNIQUE,
    sequence INTEGER NOT NULL CHECK (sequence >= 0),
    timestamp TEXT,
    kind TEXT NOT NULL,
    summary TEXT NOT NULL,
    content TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    command_text TEXT NOT NULL,
    projection_version TEXT NOT NULL,
    canonical_revision INTEGER NOT NULL CHECK (canonical_revision >= 0)
) STRICT;

CREATE INDEX search_documents_session_order
    ON search_documents(session_id, sequence, event_id);
CREATE INDEX search_documents_filter_order
    ON search_documents(timestamp DESC, session_id, sequence, event_id);

CREATE TABLE search_document_files (
    document_rowid INTEGER NOT NULL REFERENCES search_documents(rowid) ON DELETE CASCADE,
    path TEXT NOT NULL,
    PRIMARY KEY (document_rowid, path)
) STRICT;

CREATE INDEX search_document_files_path ON search_document_files(path, document_rowid);

CREATE VIRTUAL TABLE search_documents_fts USING fts5(
    summary,
    content,
    content='search_documents',
    content_rowid='rowid',
    tokenize='unicode61'
);

CREATE TRIGGER search_documents_insert AFTER INSERT ON search_documents BEGIN
    INSERT INTO search_documents_fts(rowid, summary, content)
    VALUES (new.rowid, new.summary, new.content);
END;

CREATE TRIGGER search_documents_delete AFTER DELETE ON search_documents BEGIN
    INSERT INTO search_documents_fts(search_documents_fts, rowid, summary, content)
    VALUES ('delete', old.rowid, old.summary, old.content);
END;

CREATE TRIGGER search_documents_update AFTER UPDATE ON search_documents BEGIN
    INSERT INTO search_documents_fts(search_documents_fts, rowid, summary, content)
    VALUES ('delete', old.rowid, old.summary, old.content);
    INSERT INTO search_documents_fts(rowid, summary, content)
    VALUES (new.rowid, new.summary, new.content);
END;
