package opencode

import (
	"context"
	"database/sql"
	"math"

	"github.com/pooya79/AgentSession/internal/model"
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
