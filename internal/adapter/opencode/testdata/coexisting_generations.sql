CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT, time_created INTEGER, time_updated INTEGER, extra BLOB);
CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT, extra TEXT);
CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, data TEXT, extra BLOB);
CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT, type TEXT, seq INTEGER, time_created INTEGER, time_updated INTEGER, data TEXT, extra BLOB);
CREATE TABLE event_sequence (aggregate_id TEXT PRIMARY KEY, seq INTEGER, owner_id TEXT);
CREATE TABLE event (id TEXT PRIMARY KEY, aggregate_id TEXT, seq INTEGER, type TEXT, data TEXT, extra BLOB);

INSERT INTO session VALUES ('all_complete', 'Complete durable wins', 1, 9, x'');
INSERT INTO message VALUES ('legacy_complete', 'all_complete', 2, '{"role":"user"}', '');
INSERT INTO part VALUES ('legacy_complete_part', 'legacy_complete', 'all_complete', '{"type":"text","text":"legacy"}', x'');
INSERT INTO session_message VALUES ('message_complete', 'all_complete', 'user', 1, 3, 3, '{"text":"sequenced"}', x'');
INSERT INTO event_sequence VALUES ('all_complete', 1, 'owner');
INSERT INTO event VALUES ('event_complete', 'all_complete', 1, 'session.next.synthetic', '{"timestamp":4,"text":"durable"}', x'');

INSERT INTO session VALUES ('all_incomplete', 'Incomplete durable falls back', 10, 19, x'00');
INSERT INTO message VALUES ('legacy_incomplete', 'all_incomplete', 11, '{"role":"user"}', '');
INSERT INTO part VALUES ('legacy_incomplete_part', 'legacy_incomplete', 'all_incomplete', '{"type":"text","text":"legacy fallback"}', x'00');
INSERT INTO session_message VALUES ('message_incomplete', 'all_incomplete', 'user', 1, 12, 12, '{"text":"sequenced fallback"}', x'00');
INSERT INTO event_sequence VALUES ('all_incomplete', 8, 'owner');
INSERT INTO event VALUES ('event_incomplete', 'all_incomplete', 2, 'session.next.synthetic', '{"timestamp":13,"text":"incomplete durable"}', x'00');
