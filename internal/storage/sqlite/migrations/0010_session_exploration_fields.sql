ALTER TABLE sessions ADD COLUMN last_activity_at TEXT;
ALTER TABLE sessions ADD COLUMN first_user_message TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN event_count INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0);
ALTER TABLE events ADD COLUMN message_role TEXT NOT NULL DEFAULT '';

UPDATE events
SET message_role = CASE
    WHEN json_valid(data_json) THEN COALESCE(json_extract(data_json, '$.Role'), '')
    ELSE ''
END
WHERE kind = 'message';

UPDATE sessions
SET last_activity_at = (
        SELECT CASE
            WHEN activity.value IS NULL THEN NULL
            WHEN instr(activity.value, '.') = 0 THEN substr(activity.value, 1, 19) || '.000000000Z'
            ELSE substr(activity.value, 1, 20) ||
                 substr(substr(activity.value, 21, length(activity.value) - 21) || '000000000', 1, 9) || 'Z'
        END
        FROM (
            SELECT COALESCE(
                sessions.ended_at,
                (SELECT events.timestamp
                 FROM events
                 WHERE events.session_id = sessions.id AND events.timestamp IS NOT NULL
                 ORDER BY CASE
                     WHEN instr(events.timestamp, '.') = 0 THEN substr(events.timestamp, 1, 19) || '.000000000Z'
                     ELSE substr(events.timestamp, 1, 20) ||
                          substr(substr(events.timestamp, 21, length(events.timestamp) - 21) || '000000000', 1, 9) || 'Z'
                 END DESC
                 LIMIT 1),
                sessions.started_at
            ) AS value
        ) AS activity
    ),
    first_user_message = COALESCE((
        SELECT substr(events.searchable_text, 1, 1024)
        FROM events
        WHERE events.session_id = sessions.id
          AND events.kind = 'message'
          AND events.message_role = 'user'
        ORDER BY events.sequence
        LIMIT 1
    ), ''),
    event_count = (
        SELECT COUNT(*) FROM events WHERE events.session_id = sessions.id
    );

CREATE INDEX sessions_last_activity_order ON sessions(last_activity_at DESC, id ASC);
CREATE INDEX events_session_timestamp ON events(session_id, timestamp);
