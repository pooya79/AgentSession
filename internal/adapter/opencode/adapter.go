// Package opencode imports OpenCode's SQLite session/message/part store.
package opencode

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	_ "modernc.org/sqlite"
)

const (
	AdapterVersion       model.Version = "1"
	FormatVersion        model.Version = "opencode-sqlite-message-part-v1"
	ModelVersion         model.Version = "1"
	NormalizationVersion model.Version = "1"
	CursorVersion        model.Version = "opencode-logical-cursor-v1"
	fingerprintVersion                 = "opencode-logical-fingerprint-v1"
)

var requiredColumns = map[string][]string{
	"session": {"id", "title", "time_created", "time_updated"},
	"message": {"id", "session_id", "time_created", "data"},
	"part":    {"id", "message_id", "session_id", "data"},
}

type Adapter struct{}

func New() *Adapter                     { return &Adapter{} }
func (*Adapter) Name() string           { return "opencode" }
func (*Adapter) Version() model.Version { return AdapterVersion }

func (a *Adapter) Probe(ctx context.Context, source importer.Source) (importer.ProbeResult, error) {
	if err := source.Validate(); err != nil {
		return importer.ProbeResult{}, err
	}
	if source.LocalPath == "" {
		return importer.ProbeResult{Confidence: importer.ProbeUnsupported}, nil
	}
	view, err := openSnapshot(ctx, source.LocalPath)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not a database") {
			return importer.ProbeResult{Confidence: importer.ProbeUnsupported}, nil
		}
		return importer.ProbeResult{}, fmt.Errorf("open OpenCode database for probe: %w", err)
	}
	defer view.close()
	ok, err := probeSchema(ctx, view.tx)
	if err != nil {
		return importer.ProbeResult{}, fmt.Errorf("probe OpenCode schema: %w", err)
	}
	if !ok {
		return importer.ProbeResult{Confidence: importer.ProbeUnsupported}, nil
	}
	return importer.ProbeResult{Confidence: importer.ProbeCertain, FormatVersion: FormatVersion}, nil
}

func (a *Adapter) PrepareContainer(ctx context.Context, source importer.Source) (importer.PreparedContainer, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	if source.LocalPath == "" {
		return nil, errors.New("OpenCode SQLite adapter requires a local path")
	}
	view, err := openSnapshot(ctx, source.LocalPath)
	if err != nil {
		return nil, err
	}
	ok, err := probeSchema(ctx, view.tx)
	if err != nil || !ok {
		_ = view.close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("database does not contain the required OpenCode schema")
	}
	return &container{adapter: a, source: source, snapshot: view}, nil
}

type snapshot struct {
	db   *sql.DB
	conn *sql.Conn
	tx   *sql.Tx
}

func openSnapshot(ctx context.Context, path string) (*snapshot, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA query_only = ON`); err != nil {
		conn.Close()
		db.Close()
		return nil, fmt.Errorf("enforce query-only connection: %w", err)
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		conn.Close()
		db.Close()
		return nil, fmt.Errorf("begin consistent read transaction: %w", err)
	}
	// Establish the snapshot now so later WAL commits cannot change a child view.
	var schemaVersion int64
	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
		tx.Rollback()
		conn.Close()
		db.Close()
		return nil, fmt.Errorf("establish read snapshot: %w", err)
	}
	return &snapshot{db: db, conn: conn, tx: tx}, nil
}

func (s *snapshot) close() error {
	return errors.Join(s.tx.Rollback(), s.conn.Close(), s.db.Close())
}

func probeSchema(ctx context.Context, tx *sql.Tx) (bool, error) {
	for table, required := range requiredColumns {
		rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			return false, err
		}
		columns := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return false, err
			}
			columns[name] = true
		}
		if err := rows.Close(); err != nil {
			return false, err
		}
		for _, column := range required {
			if !columns[column] {
				return false, nil
			}
		}
	}
	return true, nil
}

type container struct {
	adapter  *Adapter
	source   importer.Source
	snapshot *snapshot
	closed   bool
}

func (c *container) Children(ctx context.Context) ([]importer.PreparedChild, error) {
	rows, err := c.snapshot.tx.QueryContext(ctx, `SELECT id, title, time_created, time_updated FROM session ORDER BY time_created, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var children []importer.PreparedChild
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var nativeID string
		var title sql.NullString
		var created, updated any
		if err := rows.Scan(&nativeID, &title, &created, &updated); err != nil {
			return nil, err
		}
		childID := logicalSourceID(c.source.ID, nativeID)
		childSource := importer.Source{ID: childID, Hint: "opencode", LocalPath: c.source.LocalPath}
		meta := sessionMeta{nativeID: nativeID, title: title.String, created: created, updated: updated}
		children = append(children, importer.PreparedChild{Source: childSource, Prepared: &prepared{
			adapter: c.adapter, source: childSource, tx: c.snapshot.tx, meta: meta,
		}})
	}
	return children, rows.Err()
}

func (c *container) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.snapshot.close()
}

func logicalSourceID(containerID model.SourceID, nativeID string) model.SourceID {
	sum := sha256.Sum256([]byte("agentsession:opencode-logical-source:v1\x00" + string(containerID) + "\x00" + nativeID))
	return model.SourceID("opencode_src_" + hex.EncodeToString(sum[:]))
}

func canonicalSessionID(sourceID model.SourceID) model.SessionID {
	sum := sha256.Sum256([]byte("agentsession:opencode-session:v1\x00" + string(sourceID)))
	return model.SessionID("opencode_" + hex.EncodeToString(sum[:]))
}

type sessionMeta struct {
	nativeID string
	title    string
	created  any
	updated  any
}

type cursorState struct {
	Version string `json:"version"`
	Count   int64  `json:"count"`
	LastKey string `json:"last_key"`
}

type fingerprintState struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type prepared struct {
	adapter *Adapter
	source  importer.Source
	tx      *sql.Tx
	meta    sessionMeta
}

func (p *prepared) Close() error { return nil }

func (p *prepared) Verify(ctx context.Context, state importer.SourceState) (importer.SourceChange, error) {
	if state.Import.AdapterName != p.adapter.Name() || state.Import.AdapterVersion != AdapterVersion ||
		state.Import.FormatVersion != FormatVersion || state.Import.ModelVersion != ModelVersion ||
		state.Import.NormalizationVersion != NormalizationVersion {
		return importer.SourceReplaced, nil
	}
	var cursor cursorState
	var fingerprint fingerprintState
	if state.Checkpoint.StateVersion != CursorVersion || json.Unmarshal(state.Checkpoint.Cursor, &cursor) != nil ||
		json.Unmarshal(state.Checkpoint.Fingerprint, &fingerprint) != nil || cursor.Version != string(CursorVersion) ||
		fingerprint.Version != fingerprintVersion || cursor.Count < 0 {
		return importer.SourceReplaced, nil
	}
	digest := sha256.New()
	var count int64
	prefix := hex.EncodeToString(digest.Sum(nil))
	err := p.eachRecord(ctx, func(record logicalRecord) error {
		if count < cursor.Count {
			writeFingerprint(digest, record.raw)
			count++
			if count == cursor.Count {
				prefix = hex.EncodeToString(digest.Sum(nil))
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if count < cursor.Count {
		return importer.SourceTruncated, nil
	}
	if prefix != fingerprint.SHA256 {
		return importer.SourceMutated, nil
	}
	// Count the tail without retaining it.
	total := int64(0)
	if err := p.eachRecord(ctx, func(logicalRecord) error { total++; return nil }); err != nil {
		return "", err
	}
	if total == cursor.Count {
		return importer.SourceUnchanged, nil
	}
	return importer.SourceAppend, nil
}

func (p *prepared) Import(ctx context.Context, resume *importer.ImportCheckpoint, sink importer.ImportSink) error {
	start := int64(0)
	if resume != nil {
		var cursor cursorState
		if resume.StateVersion != CursorVersion || json.Unmarshal(resume.Cursor, &cursor) != nil || cursor.Version != string(CursorVersion) {
			return importer.ErrSourceChanged
		}
		start = cursor.Count
	}
	return p.stream(ctx, start, sink)
}

func (p *prepared) Reconcile(ctx context.Context, sink importer.ImportSink) error {
	return p.stream(ctx, 0, sink)
}

func (p *prepared) stream(ctx context.Context, start int64, sink importer.ImportSink) error {
	session := p.session()
	if err := sink.Begin(ctx, session); err != nil {
		return err
	}
	digest := sha256.New()
	recordCount, eventSequence := int64(0), int64(0)
	checkpoint, err := p.checkpoint(0, "", digest)
	if err != nil {
		return err
	}
	err = p.eachRecord(ctx, func(record logicalRecord) error {
		writeFingerprint(digest, record.raw)
		events, diagnostics, err := p.normalize(record, session.ID, eventSequence)
		if err != nil {
			return err
		}
		if record.table != "session" {
			if _, diagnostic := millisecondTime(record.timeCreated, "opencode.record.time_created.invalid"); diagnostic != nil {
				diagnostics = append(diagnostics, *diagnostic)
			}
		}
		eventSequence += int64(len(events))
		recordCount++
		checkpoint, err = p.checkpoint(recordCount, record.key, digest)
		if err != nil {
			return err
		}
		if recordCount <= start {
			return nil
		}
		sequence := recordCount - 1
		hashValue := model.HashRecord(record.raw)
		rawID, err := model.NewRawRecordID(model.RawRecordIDInput{SourceID: p.source.ID, RecordSequence: &sequence, ContentHash: hashValue})
		if err != nil {
			return err
		}
		ref := model.RawRecordRef{ID: rawID, SourceID: p.source.ID, RecordSequence: &sequence, ContentHash: hashValue}
		for i := range events {
			events[i].RawRecord = ref
		}
		for i := range diagnostics {
			diagnostics[i].RawRecordIDs = []model.RawRecordID{rawID}
			for _, event := range events {
				diagnostics[i].EventIDs = append(diagnostics[i].EventIDs, event.ID)
			}
		}
		return sink.Accept(ctx, importer.RecordEnvelope{
			RawRecord: model.RawRecord{Ref: ref, Content: append([]byte(nil), record.raw...)}, Events: events,
			Diagnostics: diagnostics, Checkpoint: checkpoint,
		})
	})
	if err != nil {
		return err
	}
	if recordCount < start {
		return importer.ErrSourceChanged
	}
	return sink.Complete(ctx, session, checkpoint)
}

func (p *prepared) checkpoint(count int64, lastKey string, digest interface{ Sum([]byte) []byte }) (importer.ImportCheckpoint, error) {
	cursor, err := json.Marshal(cursorState{Version: string(CursorVersion), Count: count, LastKey: lastKey})
	if err != nil {
		return importer.ImportCheckpoint{}, err
	}
	fingerprint, err := json.Marshal(fingerprintState{Version: fingerprintVersion, SHA256: hex.EncodeToString(digest.Sum(nil))})
	if err != nil {
		return importer.ImportCheckpoint{}, err
	}
	return importer.ImportCheckpoint{SourceID: p.source.ID, RecordSequence: count - 1, StateVersion: CursorVersion, Cursor: cursor, Fingerprint: fingerprint}, nil
}

func (p *prepared) session() model.Session {
	started, startDiagnostic := millisecondTime(p.meta.created, "opencode.session.time_created.invalid")
	ended, endDiagnostic := millisecondTime(p.meta.updated, "opencode.session.time_updated.invalid")
	var diagnostics []model.Diagnostic
	if startDiagnostic != nil {
		diagnostics = append(diagnostics, *startDiagnostic)
	}
	if endDiagnostic != nil {
		diagnostics = append(diagnostics, *endDiagnostic)
	}
	return model.Session{
		ID: canonicalSessionID(p.source.ID), Title: p.meta.title, StartedAt: started, EndedAt: ended, Diagnostics: diagnostics,
		Import: model.ImportMetadata{SourceID: p.source.ID, AdapterName: p.adapter.Name(), AdapterVersion: AdapterVersion,
			FormatVersion: FormatVersion, ModelVersion: ModelVersion, NormalizationVersion: NormalizationVersion},
	}
}

type encodedColumn struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Value  string `json:"value,omitempty"`
	Base64 string `json:"base64,omitempty"`
}

type encodedRow struct {
	Table   string          `json:"table"`
	Key     string          `json:"key"`
	Columns []encodedColumn `json:"columns"`
}

type logicalRecord struct {
	table       string
	key         string
	nativeID    string
	messageID   string
	data        []byte
	dataIsBlob  bool
	messageData []byte
	timeCreated any
	raw         []byte
}

func (p *prepared) eachRecord(ctx context.Context, accept func(logicalRecord) error) error {
	sessionRecord, err := p.selectOne(ctx, "session", `id = ?`, []any{p.meta.nativeID}, "", p.meta.nativeID, "")
	if err != nil {
		return err
	}
	if err := accept(sessionRecord); err != nil {
		return err
	}
	messages, err := p.tx.QueryContext(ctx, `SELECT * FROM message WHERE session_id = ? ORDER BY time_created, id`, p.meta.nativeID)
	if err != nil {
		return err
	}
	defer messages.Close()
	columns, err := messages.Columns()
	if err != nil {
		return err
	}
	for messages.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		values, err := scanValues(messages, len(columns))
		if err != nil {
			return err
		}
		messageRecord, err := makeRecord("message", columns, values, "", "")
		if err != nil {
			return err
		}
		messageData := append([]byte(nil), messageRecord.data...)
		parts, err := p.tx.QueryContext(ctx, `SELECT * FROM part WHERE session_id = ? AND message_id = ? ORDER BY id`, p.meta.nativeID, messageRecord.nativeID)
		if err != nil {
			return err
		}
		partColumns, err := parts.Columns()
		if err != nil {
			parts.Close()
			return err
		}
		for parts.Next() {
			partValues, err := scanValues(parts, len(partColumns))
			if err != nil {
				parts.Close()
				return err
			}
			partRecord, err := makeRecord("part", partColumns, partValues, messageRecord.key+":part", messageRecord.nativeID)
			if err != nil {
				parts.Close()
				return err
			}
			partRecord.messageData = messageData
			if err := accept(partRecord); err != nil {
				parts.Close()
				return err
			}
		}
		if err := parts.Err(); err != nil {
			parts.Close()
			return err
		}
		if err := parts.Close(); err != nil {
			return err
		}
		if err := accept(messageRecord); err != nil {
			return err
		}
	}
	return messages.Err()
}

func (p *prepared) selectOne(ctx context.Context, table, where string, args []any, key, nativeID, messageID string) (logicalRecord, error) {
	query := `SELECT * FROM ` + table + ` WHERE ` + where
	rows, err := p.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return logicalRecord{}, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return logicalRecord{}, err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return logicalRecord{}, err
		}
		return logicalRecord{}, sql.ErrNoRows
	}
	values, err := scanValues(rows, len(columns))
	if err != nil {
		return logicalRecord{}, err
	}
	record, err := makeRecord(table, columns, values, key, messageID)
	if nativeID != "" {
		record.nativeID = nativeID
	}
	return record, err
}

func scanValues(rows *sql.Rows, count int) ([]any, error) {
	values := make([]any, count)
	dest := make([]any, count)
	for i := range values {
		dest[i] = &values[i]
	}
	return values, rows.Scan(dest...)
}

func makeRecord(table string, names []string, values []any, keyPrefix, messageID string) (logicalRecord, error) {
	record := logicalRecord{table: table, messageID: messageID}
	encoded := encodedRow{Table: table, Columns: make([]encodedColumn, len(names))}
	for i, name := range names {
		column, err := encodeColumn(name, values[i])
		if err != nil {
			return logicalRecord{}, err
		}
		encoded.Columns[i] = column
		switch name {
		case "id":
			record.nativeID = stringValue(values[i])
		case "data":
			record.data, record.dataIsBlob = bytesValue(values[i])
		case "time_created":
			record.timeCreated = values[i]
		}
	}
	if table == "message" {
		record.key = "message:" + valueKey(record.timeCreated) + ":" + record.nativeID
	} else if keyPrefix == "" {
		record.key = table + ":" + record.nativeID
	} else {
		record.key = keyPrefix + ":" + record.nativeID
	}
	encoded.Key = record.key
	raw, err := json.Marshal(encoded)
	record.raw = raw
	return record, err
}

func encodeColumn(name string, value any) (encodedColumn, error) {
	column := encodedColumn{Name: name}
	switch value := value.(type) {
	case nil:
		column.Type = "null"
	case int64:
		column.Type, column.Value = "integer", strconv.FormatInt(value, 10)
	case float64:
		column.Type, column.Value = "real", strconv.FormatFloat(value, 'g', -1, 64)
	case string:
		column.Type, column.Value, column.Base64 = "text", value, base64.StdEncoding.EncodeToString([]byte(value))
	case []byte:
		column.Type, column.Base64 = "blob", base64.StdEncoding.EncodeToString(value)
	default:
		return encodedColumn{}, fmt.Errorf("unsupported SQLite value type %T in column %q", value, name)
	}
	return column, nil
}

func bytesValue(value any) ([]byte, bool) {
	switch value := value.(type) {
	case string:
		return []byte(value), false
	case []byte:
		return append([]byte(nil), value...), true
	default:
		return nil, false
	}
}

func stringValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return fmt.Sprint(value)
	}
}

func valueKey(value any) string {
	column, _ := encodeColumn("", value)
	return column.Type + ":" + column.Value + column.Base64
}

func writeFingerprint(digest interface{ Write([]byte) (int, error) }, raw []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(raw)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(raw)
}

func millisecondTime(value any, code string) (*time.Time, *model.Diagnostic) {
	var milliseconds int64
	switch value := value.(type) {
	case int64:
		milliseconds = value
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || value > math.MaxInt64 || value < math.MinInt64 {
			return nil, timestampDiagnostic(code)
		}
		milliseconds = int64(value)
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, timestampDiagnostic(code)
		}
		milliseconds = parsed
	case []byte:
		parsed, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return nil, timestampDiagnostic(code)
		}
		milliseconds = parsed
	case nil:
		return nil, nil
	default:
		return nil, timestampDiagnostic(code)
	}
	valueTime := time.UnixMilli(milliseconds).UTC()
	if valueTime.Year() < 0 || valueTime.Year() > 9999 {
		return nil, timestampDiagnostic(code)
	}
	return &valueTime, nil
}

func timestampDiagnostic(code string) *model.Diagnostic {
	return &model.Diagnostic{Code: code, Severity: model.SeverityWarning, Message: "An OpenCode millisecond timestamp is malformed; source order was preserved."}
}

func (p *prepared) normalize(record logicalRecord, sessionID model.SessionID, sequence int64) ([]model.Event, []model.Diagnostic, error) {
	if record.table == "session" {
		return nil, nil, nil
	}
	var data map[string]json.RawMessage
	if len(record.data) == 0 || json.Unmarshal(record.data, &data) != nil {
		return nil, []model.Diagnostic{{Code: "opencode.record.data.malformed", Severity: model.SeverityWarning, Message: "The OpenCode row data is malformed JSON and was retained without canonical interpretation."}}, nil
	}
	typeName := jsonString(data["type"])
	if record.table == "message" {
		return normalizeMessage(record, data, typeName, sessionID, sequence)
	}
	return normalizePart(record, data, typeName, sessionID, sequence)
}

func normalizeMessage(record logicalRecord, data map[string]json.RawMessage, typeName string, sessionID model.SessionID, sequence int64) ([]model.Event, []model.Diagnostic, error) {
	role := jsonString(data["role"])
	if role == "" && typeName != "" {
		role = typeName
	}
	if role == "" {
		event, err := newEvent(record, sessionID, sequence, 0, "unknown", model.EventKindUnknown, "OpenCode message with missing role", "", model.UnknownData{OriginalKind: "message"})
		return []model.Event{event}, []model.Diagnostic{{Code: "opencode.message.role.missing", Severity: model.SeverityWarning, Message: "The OpenCode message row has no role discriminator."}}, err
	}
	if role != "assistant" && role != "user" && role != "system" && role != "developer" {
		event, err := newEvent(record, sessionID, sequence, 0, "unknown", model.EventKindUnknown, "Unsupported OpenCode message: "+role, role, model.UnknownData{OriginalKind: "message:" + role})
		return []model.Event{event}, nil, err
	}
	if role != "assistant" {
		return nil, nil, nil
	}
	var events []model.Event
	var diagnostics []model.Diagnostic
	if raw := data["tokens"]; len(raw) > 0 {
		var tokens map[string]json.RawMessage
		if json.Unmarshal(raw, &tokens) == nil {
			var cache map[string]json.RawMessage
			_ = json.Unmarshal(tokens["cache"], &cache)
			usage := model.UsageData{InputTokens: jsonInt64(tokens["input"]), OutputTokens: jsonInt64(tokens["output"]), CacheReadTokens: jsonInt64(cache["read"]), CacheWriteTokens: jsonInt64(cache["write"])}
			if clearNegativeTokenCounters(&usage) {
				diagnostics = append(diagnostics, model.Diagnostic{Code: "opencode.message.tokens.negative", Severity: model.SeverityWarning, Message: "The OpenCode token usage contains a negative counter; malformed counters were omitted."})
			}
			event, err := newEvent(record, sessionID, sequence+int64(len(events)), uint64(len(events)), "usage", model.EventKindUsage, "OpenCode token usage", "", usage)
			if err != nil {
				return nil, nil, err
			}
			events = append(events, event)
		}
	}
	if raw := data["error"]; len(raw) > 0 && string(raw) != "null" {
		message, code := jsonText(raw), "assistant_error"
		var details map[string]json.RawMessage
		if json.Unmarshal(raw, &details) == nil {
			if value := jsonString(details["message"]); value != "" {
				message = value
			}
			if value := jsonString(details["name"]); value != "" {
				code = value
			}
		}
		event, err := newEvent(record, sessionID, sequence+int64(len(events)), uint64(len(events)), "error", model.EventKindError, "OpenCode assistant error", message, model.ErrorData{Code: code, Message: message})
		if err != nil {
			return nil, nil, err
		}
		events = append(events, event)
	}
	return events, diagnostics, nil
}

func clearNegativeTokenCounters(usage *model.UsageData) bool {
	found := false
	for _, counter := range []**int64{&usage.InputTokens, &usage.OutputTokens, &usage.CacheReadTokens, &usage.CacheWriteTokens} {
		if *counter != nil && **counter < 0 {
			*counter = nil
			found = true
		}
	}
	return found
}

func normalizePart(record logicalRecord, data map[string]json.RawMessage, typeName string, sessionID model.SessionID, sequence int64) ([]model.Event, []model.Diagnostic, error) {
	if typeName == "" {
		event, err := newEvent(record, sessionID, sequence, 0, "unknown", model.EventKindUnknown, "OpenCode part with missing type", "", model.UnknownData{OriginalKind: "part"})
		return []model.Event{event}, []model.Diagnostic{{Code: "opencode.part.type.missing", Severity: model.SeverityWarning, Message: "The OpenCode part row has no type discriminator."}}, err
	}
	switch typeName {
	case "text":
		text := jsonString(data["text"])
		role := messageRole(record.messageData)
		diagnostics := []model.Diagnostic(nil)
		if text == "" {
			diagnostics = append(diagnostics, model.Diagnostic{Code: "opencode.part.text.missing", Severity: model.SeverityWarning, Message: "The OpenCode text part has no text content."})
		}
		event, err := newEvent(record, sessionID, sequence, 0, "text", model.EventKindMessage, roleSummary(role), text, model.MessageData{Role: role, Text: text})
		return []model.Event{event}, diagnostics, err
	case "tool":
		callID, toolName := jsonString(data["callID"]), jsonString(data["tool"])
		var state map[string]json.RawMessage
		_ = json.Unmarshal(data["state"], &state)
		input := jsonText(state["input"])
		call, err := newEvent(record, sessionID, sequence, 0, "call", model.EventKindToolCall, "Tool call: "+fallback(toolName, "unknown"), input, model.ToolCallData{CallID: callID, ToolName: toolName, Input: input})
		if err != nil {
			return nil, nil, err
		}
		events := []model.Event{call}
		status := jsonString(state["status"])
		if status == "completed" || status == "error" {
			isError := status == "error"
			output := jsonText(state["output"])
			if output == "" {
				output = jsonText(state["error"])
			}
			result, err := newEvent(record, sessionID, sequence+1, 1, "result", model.EventKindToolResult, "Tool result: "+fallback(toolName, "unknown"), output, model.ToolResultData{CallID: callID, ToolName: toolName, Output: output, IsError: &isError})
			if err != nil {
				return nil, nil, err
			}
			events = append(events, result)
		}
		var diagnostics []model.Diagnostic
		if callID == "" || toolName == "" || len(data["state"]) == 0 {
			diagnostics = append(diagnostics, model.Diagnostic{Code: "opencode.part.tool.partial", Severity: model.SeverityWarning, Message: "The OpenCode tool part is incomplete; available evidence was normalized."})
		}
		return events, diagnostics, nil
	case "patch":
		text := jsonString(data["text"])
		if text == "" {
			text = jsonText(data["files"])
		}
		var paths []string
		_ = json.Unmarshal(data["files"], &paths)
		event, err := newEvent(record, sessionID, sequence, 0, "patch", model.EventKindPatch, "OpenCode patch", text, model.PatchData{Text: text, Paths: paths})
		return []model.Event{event}, nil, err
	case "reasoning", "file", "step-start", "step-finish", "snapshot", "agent", "subtask", "retry":
		event, err := newEvent(record, sessionID, sequence, 0, "unknown", model.EventKindUnknown, "Unsupported OpenCode part: "+typeName, typeName, model.UnknownData{OriginalKind: "part:" + typeName})
		return []model.Event{event}, nil, err
	default:
		event, err := newEvent(record, sessionID, sequence, 0, "unknown", model.EventKindUnknown, "Unsupported OpenCode part: "+typeName, typeName, model.UnknownData{OriginalKind: "part:" + typeName})
		return []model.Event{event}, nil, err
	}
}

func newEvent(record logicalRecord, sessionID model.SessionID, sequence int64, ordinal uint64, suffix string, kind model.EventKind, summary, searchable string, data model.NormalizedData) (model.Event, error) {
	nativeID := "opencode:" + record.table + ":" + record.nativeID
	if nativeID != "" {
		nativeID += ":" + suffix
	}
	var native *model.NativeEventIdentity
	if nativeID != "" {
		native = &model.NativeEventIdentity{Scope: model.NativeEventIDSession, SessionID: string(sessionID), EventID: nativeID}
	}
	id, err := model.NewEventID(model.EventIDInput{Native: native, SourceID: "fallback", RecordSequence: &sequence, RecordHash: "fallback", EventOrdinal: ordinal})
	if err != nil {
		return model.Event{}, err
	}
	timestamp, _ := millisecondTime(record.timeCreated, "")
	return model.Event{ID: id, SessionID: sessionID, Sequence: sequence, Timestamp: timestamp, Kind: kind, Summary: summary, SearchableText: searchable, Data: data}, nil
}

func messageRole(raw []byte) model.MessageRole {
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		return model.MessageRoleUnknown
	}
	switch jsonString(data["role"]) {
	case "user":
		return model.MessageRoleUser
	case "assistant":
		return model.MessageRoleAssistant
	case "system", "developer":
		return model.MessageRoleSystem
	case "":
		return model.MessageRoleUnknown
	default:
		return model.MessageRoleOther
	}
}

func roleSummary(role model.MessageRole) string {
	value := string(role)
	if value == "" {
		return "Unknown message"
	}
	return strings.ToUpper(value[:1]) + value[1:] + " message"
}

func jsonString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}
func jsonInt64(raw json.RawMessage) *int64 {
	var value int64
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}
func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if value := jsonString(raw); value != "" {
		return value
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
func fallback(value, alternate string) string {
	if value == "" {
		return alternate
	}
	return value
}
