package opencode

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

type prepared struct {
	adapter  *Adapter
	source   importer.Source
	tx       *sql.Tx
	meta     sessionMeta
	selected generationSelection
	evidence evidenceState
}

func (p *prepared) Close() error { return nil }

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
	p.evidence = evidenceState{}
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
		switch record.table {
		case "session":
		default:
			if record.timestampDiagnostic != nil {
				diagnostics = append(diagnostics, *record.timestampDiagnostic)
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
