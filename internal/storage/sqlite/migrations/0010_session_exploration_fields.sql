ALTER TABLE sessions ADD COLUMN last_activity_at TEXT;
ALTER TABLE sessions ADD COLUMN first_user_message TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN event_count INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0);
ALTER TABLE events ADD COLUMN message_role TEXT NOT NULL DEFAULT '';

CREATE INDEX sessions_last_activity_order ON sessions(last_activity_at DESC, id ASC);
CREATE INDEX events_session_timestamp ON events(session_id, timestamp);
