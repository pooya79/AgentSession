CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT, time_created INTEGER, time_updated INTEGER, future_text TEXT, future_blob BLOB);
CREATE TABLE event_sequence (aggregate_id TEXT PRIMARY KEY, seq INTEGER, owner_id TEXT);
CREATE TABLE event (id TEXT PRIMARY KEY, aggregate_id TEXT, seq INTEGER, type TEXT, data TEXT, future_text TEXT, future_blob BLOB);
CREATE TABLE session_input (session_id TEXT, data TEXT);
CREATE TABLE session_context_epoch (session_id TEXT, data TEXT);

INSERT INTO session VALUES ('ev_main', 'Durable events', 1700000000000, 1700000009000, '', x'');
INSERT INTO session VALUES ('ev_empty', 'Empty durable session', 1700000010000, 1700000010000, 'unknown', x'00ff');
INSERT INTO event_sequence VALUES ('ev_main', 8, 'owner');
INSERT INTO event_sequence VALUES ('ev_empty', 0, 'owner');
INSERT INTO event VALUES ('ev_prompt', 'ev_main', 1, 'session.next.prompted', '{"timestamp":1700000000100,"prompt":{"text":"hello","files":[],"agents":[]}}', 'unknown', x'0102');
INSERT INTO event VALUES ('ev_shell_start', 'ev_main', 2, 'session.next.shell.started', '{"timestamp":1700000000200,"callID":"shell-1","command":"pwd"}', '', x'');
INSERT INTO event VALUES ('ev_shell_end', 'ev_main', 3, 'session.next.shell.ended', '{"timestamp":1700000000300,"callID":"shell-1","output":"/tmp"}', '', x'03');
INSERT INTO event VALUES ('ev_tool_call', 'ev_main', 4, 'session.next.tool.called', '{"timestamp":1700000000400,"callID":"tool-1","tool":"read","input":{"path":"README.md"}}', '', x'');
INSERT INTO event VALUES ('ev_tool_ok', 'ev_main', 5, 'session.next.tool.success', '{"timestamp":1700000000500,"callID":"tool-1","tool":"read","content":[{"type":"text","text":"contents"},{"type":"json","value":{"ok":true}}]}', '', x'');
INSERT INTO event VALUES ('ev_step', 'ev_main', 6, 'session.next.step.ended', '{"timestamp":1700000000600,"finish":"stop","cost":0.1,"tokens":{"input":4,"output":2,"reasoning":1,"cache":{"read":1,"write":0}}}', '', x'');
INSERT INTO event VALUES ('ev_compact', 'ev_main', 7, 'session.next.compaction.ended', '{"timestamp":1700000000700,"text":"summary","recent":"recent","reason":"auto"}', '', x'');
INSERT INTO event VALUES ('ev_future', 'ev_main', 8, 'session.next.future', '{"timestamp":1700000000800}', '', x'00');
