PRAGMA journal_mode=WAL;
CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT, time_created INTEGER, time_updated INTEGER, extra BLOB);
CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT, extra TEXT);
CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, data TEXT, extra BLOB);
CREATE TABLE unrelated (value TEXT);

INSERT INTO session VALUES ('ses_alpha', 'Sanitized session', 1700000000000, 1700000001000, x'00ff');
INSERT INTO session VALUES ('ses_beta', 'Second session', 1700000002000, 1700000003000, NULL);
INSERT INTO message VALUES ('msg_user', 'ses_alpha', 1700000000100, '{"role":"user"}', 'future');
INSERT INTO part VALUES ('part_text', 'msg_user', 'ses_alpha', '{"type":"text","text":"hello"}', x'0102');
INSERT INTO message VALUES ('msg_assistant', 'ses_alpha', 1700000000200, '{"role":"assistant","tokens":{"input":3,"output":5,"cache":{"read":2,"write":1}}}', '');
INSERT INTO part VALUES ('part_tool', 'msg_assistant', 'ses_alpha', '{"type":"tool","callID":"call_1","tool":"shell","state":{"status":"completed","input":{"command":"pwd"},"output":"ok"}}', NULL);
INSERT INTO message VALUES ('msg_unknown', 'ses_beta', 1700000002100, '{"role":"future-role"}', '');
INSERT INTO part VALUES ('part_bad', 'msg_unknown', 'ses_beta', '{bad', x'00');
