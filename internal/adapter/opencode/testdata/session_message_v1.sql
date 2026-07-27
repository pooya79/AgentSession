CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT, time_created INTEGER, time_updated INTEGER, future_text TEXT, future_blob BLOB);
CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT, type TEXT, seq INTEGER, time_created INTEGER, time_updated INTEGER, data TEXT, future_text TEXT, future_blob BLOB);
CREATE TABLE session_input (session_id TEXT, data TEXT);
CREATE TABLE session_context_epoch (session_id TEXT, data TEXT);

INSERT INTO session VALUES ('sm_main', 'Sequenced messages', 1700000000000, 1700000009000, '', x'');
INSERT INTO session VALUES ('sm_empty', 'Empty sequenced session', 1700000010000, 1700000010000, 'unknown', x'00ff');
INSERT INTO session_message VALUES ('sm_user', 'sm_main', 'user', 1, 1700000000100, 1700000000100, '{"id":"ignored","type":"user","text":"hello"}', 'unknown', x'0102');
INSERT INTO session_message VALUES ('sm_shell', 'sm_main', 'shell', 2, 1700000000200, 1700000000300, '{"callID":"shell-1","command":"pwd","output":"/tmp"}', '', x'');
INSERT INTO session_message VALUES ('sm_assistant', 'sm_main', 'assistant', 3, 1700000000400, 1700000000500, '{"content":[{"type":"text","id":"c1","text":"done"},{"type":"reasoning","id":"c2","text":"private"},{"type":"tool","id":"call-1","name":"read","state":{"status":"completed","input":{"path":"README.md"},"content":[{"type":"text","text":"contents"}]}}],"tokens":{"input":4,"output":2,"reasoning":1,"cache":{"read":1,"write":0}}}', 'future', x'03');
INSERT INTO session_message VALUES ('sm_compact', 'sm_main', 'compaction', 4, 1700000000600, 1700000000600, '{"summary":"summary","recent":"recent","reason":"auto"}', NULL, NULL);
INSERT INTO session_message VALUES ('sm_future', 'sm_main', 'future-kind', 5, 1700000000700, 1700000000700, '{"value":true}', '', x'');
INSERT INTO session_message VALUES ('sm_bad', 'sm_main', 'user', 6, 1700000000800, 1700000000800, '{bad', '', x'00');
INSERT INTO session_message VALUES ('sm_after', 'sm_main', 'system', 7, 1700000000900, 1700000000900, '{"text":"after malformed"}', '', x'');
