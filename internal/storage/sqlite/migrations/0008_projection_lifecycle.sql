ALTER TABLE sessions ADD COLUMN canonical_revision INTEGER NOT NULL DEFAULT 0 CHECK (canonical_revision >= 0);

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

INSERT INTO session_projection_states (
    session_id, kind, status, target_version, target_revision, updated_at
)
SELECT s.id, d.kind, 'pending', d.target_version, s.canonical_revision, CURRENT_TIMESTAMP
FROM sessions s CROSS JOIN projection_definitions d;
