// Package opencode imports the supported generations of OpenCode's SQLite store.
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
	"hash"
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
	FormatVersion        model.Version = "opencode-sqlite-container-v2"
	LegacyFormatVersion  model.Version = "opencode-sqlite-message-part-v1"
	MessageFormatVersion model.Version = "opencode-sqlite-session-message-v1"
	EventFormatVersion   model.Version = "opencode-sqlite-durable-event-v1"
	ModelVersion         model.Version = "1"
	NormalizationVersion model.Version = "2"
	CursorVersion        model.Version = "opencode-logical-cursor-v2"
	fingerprintVersion                 = "opencode-logical-fingerprint-v2"
)

type generation string

const (
	generationLegacy  generation = "legacy"
	generationMessage generation = "session_message"
	generationEvent   generation = "durable_event"
)

type schemaInfo struct {
	legacy, messages, events bool
}

type generationSelection struct {
	generation        generation
	format            model.Version
	eventConvention   string
	durableIncomplete bool
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
	schema, ok, err := probeSchema(ctx, view.tx)
	if err != nil {
		return importer.ProbeResult{}, fmt.Errorf("probe OpenCode schema: %w", err)
	}
	if !ok {
		return importer.ProbeResult{Confidence: importer.ProbeUnsupported}, nil
	}
	_ = schema
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
	schema, ok, err := probeSchema(ctx, view.tx)
	if err != nil || !ok {
		_ = view.close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("database does not contain a supported OpenCode schema")
	}
	return &container{adapter: a, source: source, snapshot: view, schema: schema}, nil
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

var generationColumns = map[generation]map[string][]string{
	generationLegacy: {
		"message": {"id", "session_id", "time_created", "data"},
		"part":    {"id", "message_id", "session_id", "data"},
	},
	generationMessage: {
		"session_message": {"id", "session_id", "type", "seq", "time_created", "time_updated", "data"},
	},
	generationEvent: {
		"event_sequence": {"aggregate_id", "seq", "owner_id"},
		"event":          {"id", "aggregate_id", "seq", "type", "data"},
	},
}

func probeSchema(ctx context.Context, tx *sql.Tx) (schemaInfo, bool, error) {
	ok, err := hasContract(ctx, tx, map[string][]string{
		"session": {"id", "title", "time_created", "time_updated"},
	})
	if err != nil || !ok {
		return schemaInfo{}, false, err
	}
	var schema schemaInfo
	for gen, contract := range generationColumns {
		present, err := hasContract(ctx, tx, contract)
		if err != nil {
			return schemaInfo{}, false, err
		}
		switch gen {
		case generationLegacy:
			schema.legacy = present
		case generationMessage:
			schema.messages = present
		case generationEvent:
			schema.events = present
		}
	}
	return schema, schema.legacy || schema.messages || schema.events, nil
}

func hasContract(ctx context.Context, tx *sql.Tx, contract map[string][]string) (bool, error) {
	for table, required := range contract {
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
	schema   schemaInfo
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
		selected, err := selectGeneration(ctx, c.snapshot.tx, c.schema, nativeID)
		if err != nil {
			return nil, fmt.Errorf("select OpenCode generation for session: %w", err)
		}
		childID := logicalSourceID(c.source.ID, nativeID)
		childSource := importer.Source{ID: childID, Hint: "opencode", LocalPath: c.source.LocalPath}
		meta := sessionMeta{nativeID: nativeID, title: title.String, created: created, updated: updated}
		children = append(children, importer.PreparedChild{Source: childSource, Prepared: &prepared{
			adapter: c.adapter, source: childSource, tx: c.snapshot.tx, meta: meta, selected: selected,
		}})
	}
	return children, rows.Err()
}

func selectGeneration(ctx context.Context, tx *sql.Tx, schema schemaInfo, sessionID string) (generationSelection, error) {
	var durableRows int64
	if schema.events {
		convention, complete, rows, err := durableState(ctx, tx, sessionID)
		if err != nil {
			return generationSelection{}, err
		}
		durableRows = rows
		if complete && rows > 0 {
			return generationSelection{generation: generationEvent, format: EventFormatVersion, eventConvention: convention}, nil
		}
	}
	if schema.messages {
		count, err := rowCount(ctx, tx, `SELECT COUNT(*) FROM session_message WHERE session_id = ?`, sessionID)
		if err != nil {
			return generationSelection{}, err
		}
		if count > 0 {
			return generationSelection{generation: generationMessage, format: MessageFormatVersion}, nil
		}
	}
	if schema.legacy {
		count, err := rowCount(ctx, tx, `SELECT (SELECT COUNT(*) FROM message WHERE session_id = ?) + (SELECT COUNT(*) FROM part WHERE session_id = ?)`, sessionID, sessionID)
		if err != nil {
			return generationSelection{}, err
		}
		if count > 0 {
			return generationSelection{generation: generationLegacy, format: LegacyFormatVersion}, nil
		}
	}
	if durableRows > 0 {
		return generationSelection{generation: generationEvent, format: EventFormatVersion, eventConvention: "incomplete", durableIncomplete: true}, nil
	}
	if schema.events {
		return generationSelection{generation: generationEvent, format: EventFormatVersion, eventConvention: "empty"}, nil
	}
	if schema.messages {
		return generationSelection{generation: generationMessage, format: MessageFormatVersion}, nil
	}
	return generationSelection{generation: generationLegacy, format: LegacyFormatVersion}, nil
}

func durableState(ctx context.Context, tx *sql.Tx, sessionID string) (string, bool, int64, error) {
	var count, distinct, distinctIDs int64
	var minSeq, maxSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT seq), COUNT(DISTINCT id), MIN(seq), MAX(seq) FROM event WHERE aggregate_id = ?`, sessionID).
		Scan(&count, &distinct, &distinctIDs, &minSeq, &maxSeq); err != nil {
		return "", false, 0, err
	}
	var markerCount, markerDistinct int64
	var marker sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT seq), MIN(seq) FROM event_sequence WHERE aggregate_id = ?`, sessionID).
		Scan(&markerCount, &markerDistinct, &marker)
	if err != nil {
		return "", false, 0, err
	}
	if count == 0 {
		if markerCount == 1 && markerDistinct == 1 && marker.Valid {
			return "empty", true, 0, nil
		}
		return "", false, 0, nil
	}
	contiguous := distinct == count && distinctIDs == count && minSeq.Valid && maxSeq.Valid &&
		(minSeq.Int64 == 0 || minSeq.Int64 == 1) && count <= math.MaxInt64-minSeq.Int64+1 &&
		minSeq.Int64+count-1 == maxSeq.Int64
	if markerCount != 1 || markerDistinct != 1 || !marker.Valid || !contiguous {
		return "", false, count, nil
	}
	if marker.Int64 == maxSeq.Int64 {
		return "last_persisted", true, count, nil
	}
	if maxSeq.Int64 < math.MaxInt64 && marker.Int64 == maxSeq.Int64+1 {
		return "next_sequence", true, count, nil
	}
	return "", false, count, nil
}

func rowCount(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	var count int64
	return count, tx.QueryRowContext(ctx, query, args...).Scan(&count)
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
	Version         string `json:"version"`
	Format          string `json:"format"`
	EventConvention string `json:"event_sequence_convention,omitempty"`
	Count           int64  `json:"count"`
	LastKey         string `json:"last_key"`
}

type fingerprintState struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type prepared struct {
	adapter  *Adapter
	source   importer.Source
	tx       *sql.Tx
	meta     sessionMeta
	selected generationSelection
}

func (p *prepared) Close() error { return nil }

func (p *prepared) Verify(ctx context.Context, state importer.SourceState) (importer.SourceChange, error) {
	if state.Import.AdapterName != p.adapter.Name() || state.Import.AdapterVersion != AdapterVersion ||
		state.Import.FormatVersion != p.selected.format || state.Import.ModelVersion != ModelVersion ||
		state.Import.NormalizationVersion != NormalizationVersion {
		return importer.SourceReplaced, nil
	}
	var cursor cursorState
	var fingerprint fingerprintState
	if state.Checkpoint.StateVersion != CursorVersion || json.Unmarshal(state.Checkpoint.Cursor, &cursor) != nil ||
		json.Unmarshal(state.Checkpoint.Fingerprint, &fingerprint) != nil || cursor.Version != string(CursorVersion) ||
		cursor.Format != string(p.selected.format) || cursor.EventConvention != p.selected.eventConvention ||
		fingerprint.Version != fingerprintVersion || cursor.Count < 0 {
		return importer.SourceReplaced, nil
	}
	digest := sha256.New()
	var count int64
	var lastKey string
	prefix := hex.EncodeToString(digest.Sum(nil))
	err := p.eachRecord(ctx, func(record logicalRecord) error {
		if count < cursor.Count {
			writeFingerprint(digest, record.raw)
			count++
			lastKey = record.key
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
	if lastKey != cursor.LastKey {
		return importer.SourceMutated, nil
	}
	if prefix != fingerprint.SHA256 {
		return importer.SourceMutated, nil
	}
	var total int64
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
		if resume.StateVersion != CursorVersion || json.Unmarshal(resume.Cursor, &cursor) != nil ||
			cursor.Version != string(CursorVersion) || cursor.Format != string(p.selected.format) ||
			cursor.EventConvention != p.selected.eventConvention {
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
		if err := ctx.Err(); err != nil {
			return err
		}
		writeFingerprint(digest, record.raw)
		events, diagnostics, err := p.normalize(record, session.ID, eventSequence)
		if err != nil {
			return err
		}
		if record.table != "session" && p.selected.generation != generationEvent {
			if _, diagnostic := millisecondTime(record.timeCreated, "opencode.record.time_created.invalid"); diagnostic != nil {
				diagnostics = append(diagnostics, *diagnostic)
			}
		} else if record.table == "event" {
			var eventData map[string]json.RawMessage
			if json.Unmarshal(record.data, &eventData) == nil {
				if _, diagnostic := millisecondTime(rawScalar(eventData["timestamp"]), "opencode.event.timestamp.invalid"); diagnostic != nil {
					diagnostics = append(diagnostics, *diagnostic)
				}
			}
		}
		diagnostics = boundDiagnostics(diagnostics)
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
			RawRecord: model.RawRecord{Ref: ref, Content: append([]byte(nil), record.raw...)},
			Events:    events, Diagnostics: diagnostics, Checkpoint: checkpoint,
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

func boundDiagnostics(values []model.Diagnostic) []model.Diagnostic {
	if len(values) <= 8 {
		return values
	}
	values = append([]model.Diagnostic(nil), values[:7]...)
	return append(values, model.Diagnostic{
		Code: "opencode.diagnostics.truncated", Severity: model.SeverityWarning,
		Message: "Additional OpenCode row diagnostics were omitted.",
	})
}

func (p *prepared) checkpoint(count int64, lastKey string, digest hash.Hash) (importer.ImportCheckpoint, error) {
	cursor, err := json.Marshal(cursorState{
		Version: string(CursorVersion), Format: string(p.selected.format), EventConvention: p.selected.eventConvention,
		Count: count, LastKey: lastKey,
	})
	if err != nil {
		return importer.ImportCheckpoint{}, err
	}
	fingerprint, err := json.Marshal(fingerprintState{Version: fingerprintVersion, SHA256: hex.EncodeToString(digest.Sum(nil))})
	if err != nil {
		return importer.ImportCheckpoint{}, err
	}
	return importer.ImportCheckpoint{
		SourceID: p.source.ID, RecordSequence: count - 1, StateVersion: CursorVersion,
		Cursor: cursor, Fingerprint: fingerprint,
	}, nil
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
	if p.selected.durableIncomplete {
		diagnostics = append(diagnostics, model.Diagnostic{
			Code: "opencode.generation.durable.incomplete", Severity: model.SeverityWarning,
			Message: "The selected OpenCode durable event sequence is incomplete; available rows were imported in source sequence order.",
		})
	}
	return model.Session{
		ID: canonicalSessionID(p.source.ID), Title: p.meta.title, StartedAt: started, EndedAt: ended, Diagnostics: diagnostics,
		Import: model.ImportMetadata{
			SourceID: p.source.ID, AdapterName: p.adapter.Name(), AdapterVersion: AdapterVersion,
			FormatVersion: p.selected.format, ModelVersion: ModelVersion, NormalizationVersion: NormalizationVersion,
		},
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
	table          string
	key            string
	nativeID       string
	messageID      string
	rowType        string
	rowTypePresent bool
	rowTypeValid   bool
	data           []byte
	dataIsBlob     bool
	messageData    []byte
	timeCreated    any
	raw            []byte
}

func (p *prepared) eachRecord(ctx context.Context, accept func(logicalRecord) error) error {
	sessionRecord, err := p.selectOne(ctx, "session", `id = ?`, []any{p.meta.nativeID}, "", "")
	if err != nil {
		return err
	}
	if err := accept(sessionRecord); err != nil {
		return err
	}
	switch p.selected.generation {
	case generationMessage:
		return p.eachFlat(ctx, "session_message", `session_id = ?`, []any{p.meta.nativeID}, `seq, id`, accept)
	case generationEvent:
		return p.eachFlat(ctx, "event", `aggregate_id = ?`, []any{p.meta.nativeID}, `seq, id`, accept)
	default:
		return p.eachLegacy(ctx, accept)
	}
}

func (p *prepared) eachFlat(ctx context.Context, table, where string, args []any, order string, accept func(logicalRecord) error) error {
	rows, err := p.tx.QueryContext(ctx, `SELECT * FROM `+table+` WHERE `+where+` ORDER BY `+order, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		values, err := scanValues(rows, len(columns))
		if err != nil {
			return err
		}
		record, err := makeRecord(table, columns, values, "", "")
		if err != nil {
			return err
		}
		if err := accept(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (p *prepared) eachLegacy(ctx context.Context, accept func(logicalRecord) error) error {
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
		if err := p.eachPart(ctx, messageRecord, accept); err != nil {
			return err
		}
		if err := accept(messageRecord); err != nil {
			return err
		}
	}
	if err := messages.Err(); err != nil {
		return err
	}
	// Orphan parts are still authoritative selected-generation rows.
	rows, err := p.tx.QueryContext(ctx, `SELECT * FROM part WHERE session_id = ? AND NOT EXISTS (SELECT 1 FROM message WHERE message.id = part.message_id AND message.session_id = ?) ORDER BY id`, p.meta.nativeID, p.meta.nativeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	partColumns, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		values, err := scanValues(rows, len(partColumns))
		if err != nil {
			return err
		}
		record, err := makeRecord("part", partColumns, values, "orphan-part", "")
		if err != nil {
			return err
		}
		if err := accept(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (p *prepared) eachPart(ctx context.Context, message logicalRecord, accept func(logicalRecord) error) error {
	parts, err := p.tx.QueryContext(ctx, `SELECT * FROM part WHERE session_id = ? AND message_id = ? ORDER BY id`, p.meta.nativeID, message.nativeID)
	if err != nil {
		return err
	}
	defer parts.Close()
	columns, err := parts.Columns()
	if err != nil {
		return err
	}
	for parts.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		values, err := scanValues(parts, len(columns))
		if err != nil {
			return err
		}
		record, err := makeRecord("part", columns, values, message.key+":part", message.nativeID)
		if err != nil {
			return err
		}
		record.messageData = append([]byte(nil), message.data...)
		if err := accept(record); err != nil {
			return err
		}
	}
	return parts.Err()
}

func (p *prepared) selectOne(ctx context.Context, table, where string, args []any, key, messageID string) (logicalRecord, error) {
	rows, err := p.tx.QueryContext(ctx, `SELECT * FROM `+table+` WHERE `+where, args...)
	if err != nil {
		return logicalRecord{}, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return logicalRecord{}, err
	}
	if !rows.Next() {
		return logicalRecord{}, sql.ErrNoRows
	}
	values, err := scanValues(rows, len(columns))
	if err != nil {
		return logicalRecord{}, err
	}
	return makeRecord(table, columns, values, key, messageID)
}

func scanValues(rows *sql.Rows, count int) ([]any, error) {
	values, dest := make([]any, count), make([]any, count)
	for i := range values {
		dest[i] = &values[i]
	}
	return values, rows.Scan(dest...)
}

func makeRecord(table string, names []string, values []any, keyPrefix, messageID string) (logicalRecord, error) {
	record := logicalRecord{table: table, messageID: messageID}
	encoded := encodedRow{Table: table, Columns: make([]encodedColumn, len(names))}
	var seq any
	for i, name := range names {
		column, err := encodeColumn(name, values[i])
		if err != nil {
			return logicalRecord{}, err
		}
		encoded.Columns[i] = column
		switch name {
		case "id":
			record.nativeID = stringValue(values[i])
		case "type":
			record.rowTypePresent = true
			switch value := values[i].(type) {
			case string:
				record.rowType, record.rowTypeValid = value, true
			case []byte:
				record.rowType = string(value)
			}
		case "data":
			record.data, record.dataIsBlob = bytesValue(values[i])
		case "time_created":
			record.timeCreated = values[i]
		case "seq":
			seq = values[i]
		}
	}
	switch table {
	case "message":
		record.key = "message:" + valueKey(record.timeCreated) + ":" + record.nativeID
	case "session_message", "event":
		record.key = table + ":" + valueKey(seq) + ":" + record.nativeID
	default:
		if keyPrefix == "" {
			record.key = table + ":" + record.nativeID
		} else {
			record.key = keyPrefix + ":" + record.nativeID
		}
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
	case nil:
		return ""
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
