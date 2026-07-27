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
