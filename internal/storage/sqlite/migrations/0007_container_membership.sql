CREATE TABLE container_memberships (
    container_source_id TEXT NOT NULL,
    child_source_id TEXT NOT NULL UNIQUE,
    PRIMARY KEY (container_source_id, child_source_id)
) STRICT;

