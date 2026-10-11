package opencode

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
)

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
	table               string
	key                 string
	nativeID            string
	messageID           string
	rowType             string
	rowTypePresent      bool
	rowTypeValid        bool
	data                []byte
	dataIsBlob          bool
	messageData         []byte
	timeCreated         any
	timestamp           *time.Time
	timestampDiagnostic *model.Diagnostic
	sourceSequence      *int64
	sequenceDiagnostics []model.Diagnostic
	raw                 []byte
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
	var remaining, markerCount, markerDistinct int64
	var marker sql.NullInt64
	if table == "event" && p.selected.durableIncomplete {
		if err := p.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM event WHERE aggregate_id=?`, p.meta.nativeID).Scan(&remaining); err != nil {
			return fmt.Errorf("read OpenCode durable row count: %w", err)
		}
		if err := p.tx.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT seq), MIN(seq) FROM event_sequence WHERE aggregate_id=?`, p.meta.nativeID).Scan(&markerCount, &markerDistinct, &marker); err != nil {
			return fmt.Errorf("read OpenCode durable sequence marker: %w", err)
		}
	}
	var previous *int64
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
		if table == "event" && p.selected.durableIncomplete {
			gap := func(boundary model.InterpretationBoundary) {
				record.sequenceDiagnostics = append(record.sequenceDiagnostics, model.Diagnostic{
					Code: "opencode.generation.durable.gap", Severity: model.SeverityWarning,
					Message:              "The recorded source sequence leaves a possible gap in command or change evidence.",
					InterpretationReason: model.InterpretationIncompleteSourceSequence, InterpretationBoundary: boundary,
				})
			}
			current := record.sourceSequence
			switch {
			case current == nil || *current < 0 || (previous != nil && *current <= *previous):
				gap(model.InterpretationUnlocated)
			case previous == nil && *current > 1:
				gap("")
			case previous != nil && *previous < math.MaxInt64 && *current > *previous+1:
				gap("")
			}
			remaining--
			if remaining == 0 {
				if markerCount != 1 || markerDistinct != 1 || !marker.Valid || current == nil || marker.Int64 < *current {
					gap(model.InterpretationUnlocated)
				} else if *current < math.MaxInt64 && marker.Int64 > *current+1 {
					gap(model.InterpretationAfterRecord)
				}
			}
			previous = current
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
			if value, ok := values[i].(int64); ok {
				record.sourceSequence = &value
			}
		}
	}
	timestampValue := record.timeCreated
	timestampCode := "opencode.record.time_created.invalid"
	if table == "event" {
		var eventData map[string]json.RawMessage
		if json.Unmarshal(record.data, &eventData) == nil {
			timestampValue = rawScalar(eventData["timestamp"])
		}
		timestampCode = "opencode.event.timestamp.invalid"
	}
	record.timestamp, record.timestampDiagnostic = millisecondTime(timestampValue, timestampCode)
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
