-- Synthetic modern mutation application results; no live files are inspected.
CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT, time_created INTEGER, time_updated INTEGER);
CREATE TABLE event_sequence (aggregate_id TEXT PRIMARY KEY, seq INTEGER, owner_id TEXT);
CREATE TABLE event (id TEXT PRIMARY KEY, aggregate_id TEXT, seq INTEGER, type TEXT, data TEXT);
INSERT INTO session VALUES ('mutations', 'Confirmed modern mutations', 1, 9);
INSERT INTO event_sequence VALUES ('mutations', 4, 'owner');
INSERT INTO event VALUES ('mutation_1', 'mutations', 1, 'session.next.tool.called', '{"callID":"edit","tool":"edit","input":{"path":"/workspace/example/edit.go","oldString":"old","newString":"new"}}');
INSERT INTO event VALUES ('mutation_2', 'mutations', 2, 'session.next.tool.success', '{"callID":"edit","content":[],"structured":{"files":[{"file":"/workspace/example/edit.go","patch":"@@ -1 +1 @@\n-old\n+new","status":"modified","additions":1,"deletions":1}],"replacements":1}}');
INSERT INTO event VALUES ('mutation_3', 'mutations', 3, 'session.next.tool.called', '{"callID":"patch","tool":"apply_patch","input":{"patchText":"*** Begin Patch\n*** Add File: /workspace/example/add.go\n+package example\n*** End Patch"}}');
INSERT INTO event VALUES ('mutation_4', 'mutations', 4, 'session.next.tool.success', '{"callID":"patch","content":[],"structured":{"applied":[{"type":"add","target":"/workspace/example/add.go","resource":"add.go"},{"type":"delete","target":"/workspace/example/delete.go","resource":"delete.go"}],"files":[]}}');
