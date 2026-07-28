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
