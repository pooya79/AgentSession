// Package codex implements discovery and streaming import of Codex CLI rollout
// JSONL files.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

const (
	// AdapterVersion identifies the persisted Codex adapter implementation.
	AdapterVersion model.Version = "1"
	// ModelVersion identifies the canonical event model emitted by this adapter.
	ModelVersion model.Version = "1"
	// NormalizationVersion identifies the Codex-to-canonical normalization rules.
	NormalizationVersion model.Version = "1"
	// CursorVersion identifies the persisted Codex rollout cursor schema.
	CursorVersion model.Version = "codex-rollout-cursor-v1"
	// FingerprintVersion identifies the persisted Codex source fingerprint schema.
	FingerprintVersion model.Version = "codex-rollout-fingerprint-v1"

	formatLegacy         = "codex-rollout-jsonl-v1"
	formatOrdinal        = "codex-rollout-jsonl-v2-ordinal"
	readBuffer           = 32 << 10
	initialRecords       = 8
	inspectionPrefix     = 64 << 10
	maxRecordDiagnostics = 8
)

// Adapter probes and imports Codex CLI rollout files.
type Adapter struct{}

// New returns a Codex rollout adapter.
func New() *Adapter { return &Adapter{} }

// Name returns the stable adapter name persisted with imported sessions.
func (*Adapter) Name() string { return "codex" }

// Version returns the persisted Codex adapter implementation version.
func (*Adapter) Version() model.Version { return AdapterVersion }

// Probe inspects a bounded initial record window to identify a Codex rollout.
func (a *Adapter) Probe(ctx context.Context, source importer.Source) (importer.ProbeResult, error) {
	if err := source.Validate(); err != nil {
		return importer.ProbeResult{}, err
	}
	r, err := source.OpenFrom(ctx, 0)
	if err != nil {
		return importer.ProbeResult{}, fmt.Errorf("open rollout for probe: %w", err)
	}
	defer r.Close()
	inspection, err := inspectInitial(ctx, bufio.NewReaderSize(r, readBuffer), nil)
	if err != nil {
		return importer.ProbeResult{}, fmt.Errorf("inspect rollout probe: %w", err)
	}
	if !inspection.valid && !inspection.possibleMalformed {
		return importer.ProbeResult{Confidence: importer.ProbeUnsupported, Diagnostics: inspection.diagnostics}, nil
	}
	confidence := importer.ProbePossible
	if inspection.recognized {
		confidence = importer.ProbeCertain
	}
	return importer.ProbeResult{
		Confidence:    confidence,
		FormatVersion: compositeFormat(inspection.ordinal, inspection.cliVersion),
		Diagnostics:   inspection.diagnostics,
	}, nil
}

// Prepare opens a Codex rollout for verification, reconciliation, or import.
func (a *Adapter) Prepare(ctx context.Context, source importer.Source) (importer.PreparedSource, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	stream, err := source.OpenFrom(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("open rollout: %w", err)
	}
	replay, err := os.CreateTemp("", "agentsession-codex-window-*")
	if err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("create rollout replay file: %w", err)
	}
	cleanup := func() {
		_ = errors.Join(stream.Close(), replay.Close(), os.Remove(replay.Name()))
	}
	reader := bufio.NewReaderSize(stream, readBuffer)
	replayWriter := bufio.NewWriterSize(replay, readBuffer)
	inspection, err := inspectInitial(ctx, reader, replayWriter)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("inspect rollout header window: %w", err)
	}
	if err := replayWriter.Flush(); err != nil {
		cleanup()
		return nil, fmt.Errorf("flush rollout replay file: %w", err)
	}
	if _, err := replay.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, fmt.Errorf("rewind rollout replay file: %w", err)
	}
	view := &prepared{
		adapter:      a,
		source:       source,
		sourceStream: stream,
		replay:       replay,
		reader:       bufio.NewReaderSize(io.MultiReader(replay, reader), readBuffer),
		ordinal:      inspection.ordinal,
		cliVersion:   inspection.cliVersion,
		sessionID:    inspection.sessionID,
		startedAt:    inspection.startedAt,
		diagnostics:  sessionInspectionDiagnostics(inspection.diagnostics),
	}
	if view.sessionID == "" {
		sum := sha256.Sum256([]byte(source.ID))
		view.sessionID = "codex_" + hex.EncodeToString(sum[:])
	}
	view.format = compositeFormat(view.ordinal, view.cliVersion)
	return view, nil
}

type wireRecord struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   *uint64         `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	Timestamp  string `json:"timestamp"`
	CLIVersion string `json:"cli_version"`
}

type initialInspection struct {
	recognized        bool
	valid             bool
	ordinal           bool
	possibleMalformed bool
	cliVersion        string
	sessionID         string
	startedAt         *time.Time
	diagnostics       []model.Diagnostic
}

func inspectInitial(ctx context.Context, reader *bufio.Reader, replay io.Writer) (initialInspection, error) {
	result := initialInspection{cliVersion: "unknown"}
	for recordIndex := 0; recordIndex < initialRecords; recordIndex++ {
		prefix := make([]byte, 0, inspectionPrefix)
		for {
			if err := ctx.Err(); err != nil {
				return initialInspection{}, err
			}
			fragment, err := reader.ReadSlice('\n')
			if len(fragment) > 0 {
				if replay != nil {
					if _, writeErr := replay.Write(fragment); writeErr != nil {
						return initialInspection{}, fmt.Errorf("write rollout replay file: %w", writeErr)
					}
				}
				if remaining := inspectionPrefix - len(prefix); remaining > 0 {
					prefix = append(prefix, fragment[:min(remaining, len(fragment))]...)
				}
			}
			if err == nil {
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) {
				if len(prefix) > 0 {
					result.possibleMalformed = result.possibleMalformed || looksCodexLike(prefix)
				}
				return result, nil
			}
			return initialInspection{}, err
		}
		if len(prefix) == inspectionPrefix && prefix[len(prefix)-1] != '\n' {
			result.possibleMalformed = result.possibleMalformed || looksCodexLike(prefix)
			if header, ok := boundedRecordHeader(prefix); ok {
				result.valid = true
				if knownTopLevel(header.Type) {
					result.recognized = true
				}
				if header.Ordinal != nil {
					result.ordinal = true
				}
				if result.sessionID == "" && header.Type == "session_meta" {
					if meta, ok := boundedSessionMeta(prefix); ok {
						applySessionMeta(&result, meta, header.Timestamp)
					}
				}
			}
			continue
		}
		line := trimLineEnding(prefix)
		var object map[string]json.RawMessage
		if json.Unmarshal(line, &object) != nil || object == nil {
			result.possibleMalformed = result.possibleMalformed || looksCodexLike(line)
			result.diagnostics = append(result.diagnostics, probeMalformedDiagnostic())
			continue
		}
		result.valid = true
		typeName := rawString(object["type"])
		if knownTopLevel(typeName) {
			result.recognized = true
		}
		if _, ok := validUint64(object["ordinal"]); ok {
			result.ordinal = true
		}
		if result.sessionID != "" || typeName != "session_meta" {
			continue
		}
		var payload sessionMeta
		if raw, ok := object["payload"]; !ok || json.Unmarshal(raw, &payload) != nil {
			continue
		}
		applySessionMeta(&result, payload, rawString(object["timestamp"]))
	}
	return result, nil
}

func validUint64(raw json.RawMessage) (uint64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var value uint64
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func boundedRecordHeader(prefix []byte) (wireRecord, bool) {
	payloadKey := bytes.Index(prefix, []byte(`"payload"`))
	if payloadKey < 0 {
		return wireRecord{}, false
	}
	colonOffset := bytes.IndexByte(prefix[payloadKey+len(`"payload"`):], ':')
	if colonOffset < 0 {
		return wireRecord{}, false
	}
	colon := payloadKey + len(`"payload"`) + colonOffset
	candidate := make([]byte, 0, colon+len(":null}"))
	candidate = append(candidate, prefix[:colon+1]...)
	candidate = append(candidate, []byte("null}")...)
	var header wireRecord
	if json.Unmarshal(candidate, &header) != nil {
		return wireRecord{}, false
	}
	return header, true
}

func boundedSessionMeta(prefix []byte) (sessionMeta, bool) {
	payloadKey := bytes.Index(prefix, []byte(`"payload"`))
	if payloadKey < 0 {
		return sessionMeta{}, false
	}
	payload := prefix[payloadKey+len(`"payload"`):]
	meta := sessionMeta{}
	meta.SessionID, _ = boundedStringField(payload, "session_id")
	meta.ID, _ = boundedStringField(payload, "id")
	meta.CLIVersion, _ = boundedStringField(payload, "cli_version")
	meta.Timestamp, _ = boundedStringField(payload, "timestamp")
	if strings.TrimSpace(meta.SessionID) == "" && strings.TrimSpace(meta.ID) == "" {
		return sessionMeta{}, false
	}
	return meta, true
}

func boundedStringField(data []byte, key string) (string, bool) {
	keyOffset := bytes.Index(data, []byte(`"`+key+`"`))
	if keyOffset < 0 {
		return "", false
	}
	rest := data[keyOffset+len(key)+2:]
	colon := bytes.IndexByte(rest, ':')
	if colon < 0 {
		return "", false
	}
	rest = bytes.TrimLeft(rest[colon+1:], " \t\r\n")
	if len(rest) == 0 || rest[0] != '"' {
		return "", false
	}
	escaped := false
	for index := 1; index < len(rest); index++ {
		switch {
		case escaped:
			escaped = false
		case rest[index] == '\\':
			escaped = true
		case rest[index] == '"':
			var value string
			if json.Unmarshal(rest[:index+1], &value) != nil {
				return "", false
			}
			return value, true
		}
	}
	return "", false
}

func applySessionMeta(result *initialInspection, meta sessionMeta, recordTimestamp string) {
	sessionID := strings.TrimSpace(meta.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(meta.ID)
	}
	if sessionID == "" {
		return
	}
	result.sessionID = sessionID
	if cli := strings.TrimSpace(meta.CLIVersion); cli != "" {
		result.cliVersion = cli
	}
	timestamp := strings.TrimSpace(meta.Timestamp)
	if timestamp == "" {
		timestamp = strings.TrimSpace(recordTimestamp)
	}
	if timestamp == "" {
		return
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		result.diagnostics = append(result.diagnostics, model.Diagnostic{
			Code: "codex.session.timestamp.invalid", Severity: model.SeverityWarning,
			Message: "The Codex session metadata timestamp is malformed.",
		})
		return
	}
	result.startedAt = &parsed
}

func probeMalformedDiagnostic() model.Diagnostic {
	return model.Diagnostic{
		Code: "codex.probe.malformed", Severity: model.SeverityWarning,
		Message: "A complete JSONL record in the initial inspection window is malformed.",
	}
}

func sessionInspectionDiagnostics(diagnostics []model.Diagnostic) []model.Diagnostic {
	var result []model.Diagnostic
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "codex.session.timestamp.invalid" {
			result = append(result, diagnostic)
		}
	}
	return result
}

type cursorState struct {
	Version string `json:"version"`
	Offset  int64  `json:"offset"`
}

type fingerprintState struct {
	Version   string `json:"version"`
	SessionID string `json:"session_id"`
	SHA256    string `json:"sha256"`
}

type prepared struct {
	adapter      *Adapter
	source       importer.Source
	sourceStream io.ReadCloser
	replay       *os.File
	reader       *bufio.Reader
	ordinal      bool
	cliVersion   string
	sessionID    string
	format       model.Version
	startedAt    *time.Time
	diagnostics  []model.Diagnostic

	offset   int64
	seq      int64
	eventSeq int64
	digest   hash.Hash
	spool    *os.File
	closed   bool
}

func (p *prepared) Verify(ctx context.Context, state importer.SourceState) (importer.SourceChange, error) {
	if state.Import.AdapterName != p.adapter.Name() || state.Import.AdapterVersion != p.adapter.Version() ||
		state.Import.FormatVersion != p.format || state.Import.ModelVersion != ModelVersion || state.Import.NormalizationVersion != NormalizationVersion {
		return importer.SourceReplaced, nil
	}
	var cursor cursorState
	var fingerprint fingerprintState
	if state.Checkpoint.StateVersion != CursorVersion || json.Unmarshal(state.Checkpoint.Cursor, &cursor) != nil ||
		json.Unmarshal(state.Checkpoint.Fingerprint, &fingerprint) != nil || cursor.Version != string(CursorVersion) ||
		fingerprint.Version != string(FingerprintVersion) {
		return importer.SourceReplaced, nil
	}
	if fingerprint.SessionID != p.sessionID {
		return importer.SourceReplaced, nil
	}
	if cursor.Offset > p.source.Size {
		return importer.SourceTruncated, nil
	}
	if cursor.Offset < 0 {
		return importer.SourceMutated, nil
	}
	if err := p.consumePrefix(ctx, cursor.Offset); err != nil {
		return "", err
	}
	if hex.EncodeToString(p.digest.Sum(nil)) != fingerprint.SHA256 {
		return importer.SourceMutated, nil
	}
	p.seq = state.Checkpoint.RecordSequence + 1
	if state.LastEventSequence != nil {
		p.eventSeq = *state.LastEventSequence + 1
	}
	if cursor.Offset == p.source.Size {
		return importer.SourceUnchanged, nil
	}
	return importer.SourceAppend, nil
}

func (p *prepared) consumePrefix(ctx context.Context, target int64) error {
	p.digest = sha256.New()
	spool, err := os.CreateTemp("", "agentsession-codex-prefix-*")
	if err != nil {
		return fmt.Errorf("create verification spool: %w", err)
	}
	p.spool = spool
	remaining := target
	buf := make([]byte, readBuffer)
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buf))
		if remaining < want {
			want = remaining
		}
		n, readErr := io.ReadFull(p.reader, buf[:want])
		if n > 0 {
			if err := p.writeVerified(buf[:n]); err != nil {
				return err
			}
			remaining -= int64(n)
		}
		if readErr != nil {
			return fmt.Errorf("verify checkpoint prefix: %w", readErr)
		}
	}
	p.offset = target
	return nil
}

func (p *prepared) writeVerified(data []byte) error {
	if _, err := p.digest.Write(data); err != nil {
		return err
	}
	if _, err := p.spool.Write(data); err != nil {
		return fmt.Errorf("spool verified prefix: %w", err)
	}
	return nil
}

func (p *prepared) Import(ctx context.Context, resume *importer.ImportCheckpoint, sink importer.ImportSink) error {
	if resume != nil && p.digest == nil {
		return fmt.Errorf("resume import requires prior verification")
	}
	if p.digest == nil {
		p.digest = sha256.New()
		p.seq = 0
	}
	return p.stream(ctx, sink, false)
}

func (p *prepared) Reconcile(ctx context.Context, sink importer.ImportSink) error {
	var source io.Reader
	if p.spool != nil {
		if _, err := p.spool.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind verified prefix: %w", err)
		}
		source = io.MultiReader(p.spool, p.reader)
	} else {
		source = p.reader
	}
	p.reader = bufio.NewReaderSize(source, readBuffer)
	p.offset = 0
	p.seq = 0
	p.eventSeq = 0
	p.digest = sha256.New()
	return p.stream(ctx, sink, true)
}

func (p *prepared) stream(ctx context.Context, sink importer.ImportSink, reconciling bool) error {
	session := model.Session{ID: model.SessionID(p.sessionID), Import: model.ImportMetadata{
		SourceID: p.source.ID, AdapterName: p.adapter.Name(), AdapterVersion: p.adapter.Version(),
		FormatVersion: p.format, ModelVersion: ModelVersion, NormalizationVersion: NormalizationVersion,
	}, StartedAt: p.startedAt, Diagnostics: append([]model.Diagnostic(nil), p.diagnostics...)}
	if err := sink.Begin(ctx, session); err != nil {
		return err
	}
	checkpoint, err := p.checkpoint(p.seq - 1)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, complete, readErr := p.nextLine()
		if len(line) > 0 && complete {
			start := p.offset
			if _, err := p.digest.Write(line); err != nil {
				return err
			}
			p.offset += int64(len(line))
			envelope, normalizeErr := p.envelope(line, start, p.seq, p.eventSeq, session)
			if normalizeErr != nil {
				return normalizeErr
			}
			p.eventSeq += int64(len(envelope.Events))
			envelope.Checkpoint, err = p.checkpoint(p.seq)
			if err != nil {
				return err
			}
			if err := sink.Accept(ctx, envelope); err != nil {
				return err
			}
			checkpoint = envelope.Checkpoint
			p.seq++
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return sink.Complete(ctx, session, checkpoint)
			}
			return fmt.Errorf("read rollout record: %w", readErr)
		}
		if reconciling && !complete {
			return sink.Complete(ctx, session, checkpoint)
		}
	}
}

func (p *prepared) nextLine() ([]byte, bool, error) {
	return readLine(p.reader)
}

func (p *prepared) checkpoint(sequence int64) (importer.ImportCheckpoint, error) {
	cursor, err := json.Marshal(cursorState{Version: string(CursorVersion), Offset: p.offset})
	if err != nil {
		return importer.ImportCheckpoint{}, err
	}
	fingerprint, err := json.Marshal(fingerprintState{Version: string(FingerprintVersion), SessionID: p.sessionID, SHA256: hex.EncodeToString(p.digest.Sum(nil))})
	if err != nil {
		return importer.ImportCheckpoint{}, err
	}
	return importer.ImportCheckpoint{SourceID: p.source.ID, RecordSequence: sequence, StateVersion: CursorVersion, Cursor: cursor, Fingerprint: fingerprint}, nil
}

func (p *prepared) envelope(line []byte, offset, sequence, eventSequence int64, session model.Session) (importer.RecordEnvelope, error) {
	seq := sequence
	rangeValue := model.ByteRange{Offset: offset, Length: int64(len(line))}
	hashValue := model.HashRecord(line)
	rawID, err := model.NewRawRecordID(model.RawRecordIDInput{SourceID: p.source.ID, RecordSequence: &seq, ByteRange: &rangeValue, ContentHash: hashValue})
	if err != nil {
		return importer.RecordEnvelope{}, err
	}
	ref := model.RawRecordRef{ID: rawID, SourceID: p.source.ID, RecordSequence: &seq, ByteRange: &rangeValue, ContentHash: hashValue}
	envelope := importer.RecordEnvelope{RawRecord: model.RawRecord{Ref: ref, Content: append([]byte(nil), line...)}}
	var record wireRecord
	if err := json.Unmarshal(trimLineEnding(line), &record); err != nil {
		envelope.Diagnostics = []model.Diagnostic{{
			Code: "codex.record.malformed", Severity: model.SeverityWarning,
			Message:              "A complete Codex rollout record is malformed JSON and was retained.",
			InterpretationReason: model.InterpretationStructurallyInvalidKnownRecord,
			RawRecordIDs:         []model.RawRecordID{rawID},
		}}
		return envelope, nil
	}
	normalizedEvents, diagnostics, err := p.normalize(record, ref, eventSequence, session.ID)
	if err != nil {
		return importer.RecordEnvelope{}, err
	}
	envelope.Events = normalizedEvents
	for i := range diagnostics {
		diagnostics[i].RawRecordIDs = []model.RawRecordID{rawID}
		if len(normalizedEvents) > 0 {
			diagnostics[i].EventIDs = make([]model.EventID, 0, len(normalizedEvents))
			for _, event := range normalizedEvents {
				diagnostics[i].EventIDs = append(diagnostics[i].EventIDs, event.ID)
			}
		}
	}
	envelope.Diagnostics = boundDiagnostics(diagnostics)
	return envelope, nil
}

func (p *prepared) normalize(record wireRecord, ref model.RawRecordRef, eventSequence int64, sessionID model.SessionID) ([]model.Event, []model.Diagnostic, error) {
	if record.Type == "session_meta" {
		if _, ok := objectValue(record.Payload); !ok {
			result := invalidKnown()
			return nil, result.diagnostics, nil
		}
		return nil, nil, nil
	}
	if strings.TrimSpace(record.Type) == "" {
		return nil, []model.Diagnostic{{
			Code: "codex.record.type.missing", Severity: model.SeverityWarning,
			Message:              "The complete Codex record has no type discriminant and was retained.",
			InterpretationReason: model.InterpretationMissingDiscriminant,
		}}, nil
	}
	result := normalizeRecord(record, p.ordinal, string(sessionID))
	if !result.handled {
		label := record.Type
		reason := model.UnknownUnsupportedRecordKind
		if record.Type == "response_item" || record.Type == "event_msg" {
			label += ":" + result.nested
			reason = model.UnknownUnsupportedNestedVariant
		}
		label = model.BoundOriginalKind(label)
		result.drafts = []eventDraft{{
			kind: model.EventKindUnknown, summary: "Unsupported Codex record: " + label,
			searchable: label,
			data: model.UnknownData{
				Reason: reason, OriginalKind: label,
			},
		}}
	}
	events := make([]model.Event, 0, len(result.drafts))
	for ordinal, draft := range result.drafts {
		eventID, err := model.NewEventID(model.EventIDInput{
			Native: draft.native, SourceID: ref.SourceID, RecordSequence: ref.RecordSequence,
			ByteRange: ref.ByteRange, RecordHash: ref.ContentHash, EventOrdinal: uint64(ordinal),
		})
		if err != nil {
			return nil, nil, err
		}
		events = append(events, model.Event{
			ID: eventID, SessionID: sessionID, Sequence: eventSequence + int64(ordinal),
			Kind: draft.kind, Summary: draft.summary, SearchableText: draft.searchable,
			Data: draft.data, RawRecord: ref,
		})
	}
	diagnostics := append([]model.Diagnostic(nil), result.diagnostics...)
	if strings.TrimSpace(record.Timestamp) != "" && len(events) > 0 {
		parsed, err := time.Parse(time.RFC3339Nano, record.Timestamp)
		if err != nil {
			diagnostics = append(diagnostics, model.Diagnostic{
				Code: "codex.timestamp.invalid", Severity: model.SeverityWarning,
				Message: "The rollout timestamp is malformed; source order was preserved.",
			})
		} else {
			for index := range events {
				events[index].Timestamp = &parsed
			}
		}
	}
	return events, diagnostics, nil
}

func boundDiagnostics(diagnostics []model.Diagnostic) []model.Diagnostic {
	if len(diagnostics) <= maxRecordDiagnostics {
		return diagnostics
	}
	result := append([]model.Diagnostic(nil), diagnostics[:maxRecordDiagnostics-1]...)
	result = append(result, model.Diagnostic{
		Code: "codex.record.diagnostics.truncated", Severity: model.SeverityWarning,
		Message:      "Additional Codex record diagnostics were omitted deterministically.",
		RawRecordIDs: append([]model.RawRecordID(nil), diagnostics[0].RawRecordIDs...),
	})
	return result
}

func (p *prepared) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	var err error
	if p.sourceStream != nil {
		err = p.sourceStream.Close()
	}
	if p.replay != nil {
		name := p.replay.Name()
		err = errors.Join(err, p.replay.Close(), os.Remove(name))
	}
	if p.spool != nil {
		name := p.spool.Name()
		err = errors.Join(err, p.spool.Close(), os.Remove(name))
	}
	return err
}

func readLine(reader *bufio.Reader) ([]byte, bool, error) {
	var record []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		record = append(record, fragment...)
		if err == nil {
			return record, true, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return record, false, io.EOF
		}
		return record, false, err
	}
}

func trimLineEnding(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	return bytes.TrimSuffix(line, []byte{'\r'})
}

func compositeFormat(ordinal bool, cli string) model.Version {
	base := formatLegacy
	if ordinal {
		base = formatOrdinal
	}
	cli = strings.TrimSpace(cli)
	if cli == "" {
		cli = "unknown"
	}
	return model.Version(base + "+cli-" + cli)
}

func knownTopLevel(kind string) bool {
	switch kind {
	case "session_meta", "response_item", "event_msg", "compacted", "turn_context", "world_state", "inter_agent_communication", "inter_agent_communication_metadata":
		return true
	default:
		return false
	}
}

func looksCodexLike(line []byte) bool {
	for _, label := range []string{"session_meta", "response_item", "event_msg", "compacted", "turn_context", "world_state"} {
		if bytes.Contains(line, []byte(`"type":"`+label)) || bytes.Contains(line, []byte(`"type": "`+label)) {
			return true
		}
	}
	return false
}
