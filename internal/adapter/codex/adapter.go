// Package codex imports Codex CLI rollout JSONL files.
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
	AdapterVersion       model.Version = "1"
	ModelVersion         model.Version = "1"
	NormalizationVersion model.Version = "1"
	CursorVersion        model.Version = "codex-rollout-cursor-v1"
	FingerprintVersion   model.Version = "codex-rollout-fingerprint-v1"

	formatLegacy  = "codex-rollout-jsonl-v1"
	formatOrdinal = "codex-rollout-jsonl-v2-ordinal"
	readBuffer    = 32 << 10
)

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (*Adapter) Name() string           { return "codex" }
func (*Adapter) Version() model.Version { return AdapterVersion }

func (a *Adapter) Probe(ctx context.Context, source importer.Source) (importer.ProbeResult, error) {
	if err := source.Validate(); err != nil {
		return importer.ProbeResult{}, err
	}
	r, err := source.OpenFrom(ctx, 0)
	if err != nil {
		return importer.ProbeResult{}, fmt.Errorf("open rollout for probe: %w", err)
	}
	defer r.Close()
	reader := bufio.NewReaderSize(r, readBuffer)
	recognized, valid, ordinal := false, false, false
	possibleMalformed := false
	cli := "unknown"
	var diagnostics []model.Diagnostic
	for i := 0; i < 8; i++ {
		if err := ctx.Err(); err != nil {
			return importer.ProbeResult{}, err
		}
		line, complete, readErr := readLine(reader)
		if len(line) == 0 && readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return importer.ProbeResult{}, fmt.Errorf("read rollout probe: %w", readErr)
		}
		if !complete {
			possibleMalformed = possibleMalformed || looksCodexLike(line)
			break
		}
		var record wireRecord
		if err := json.Unmarshal(trimLineEnding(line), &record); err != nil {
			diagnostics = append(diagnostics, model.Diagnostic{Code: "codex.probe.malformed", Severity: model.SeverityWarning, Message: "A complete JSONL record is malformed."})
			possibleMalformed = possibleMalformed || looksCodexLike(line)
			continue
		}
		valid = true
		if record.Ordinal != nil {
			ordinal = true
		}
		if knownTopLevel(record.Type) {
			recognized = true
		}
		if record.Type == "session_meta" {
			var meta sessionMeta
			if json.Unmarshal(record.Payload, &meta) == nil && strings.TrimSpace(meta.CLIVersion) != "" {
				cli = strings.TrimSpace(meta.CLIVersion)
			}
		}
		if readErr != nil {
			break
		}
	}
	if !valid && !possibleMalformed {
		return importer.ProbeResult{Confidence: importer.ProbeUnsupported, Diagnostics: diagnostics}, nil
	}
	confidence := importer.ProbePossible
	if recognized {
		confidence = importer.ProbeCertain
	}
	return importer.ProbeResult{Confidence: confidence, FormatVersion: compositeFormat(ordinal, cli), Diagnostics: diagnostics}, nil
}

func (a *Adapter) Prepare(ctx context.Context, source importer.Source) (importer.PreparedSource, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	stream, err := source.OpenFrom(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("open rollout: %w", err)
	}
	view := &prepared{adapter: a, source: source, sourceStream: stream, reader: bufio.NewReaderSize(stream, readBuffer)}
	view.first, view.firstComplete, err = readLine(view.reader)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = stream.Close()
		return nil, fmt.Errorf("read rollout header: %w", err)
	}
	view.inspectHeader()
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
	Timestamp  string `json:"timestamp"`
	CLIVersion string `json:"cli_version"`
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
	adapter       *Adapter
	source        importer.Source
	sourceStream  io.ReadCloser
	reader        *bufio.Reader
	first         []byte
	firstComplete bool
	firstUsed     bool
	ordinal       bool
	cliVersion    string
	sessionID     string
	format        model.Version
	startedAt     *time.Time
	diagnostics   []model.Diagnostic

	offset int64
	seq    int64
	digest hash.Hash
	spool  *os.File
	closed bool
}

func (p *prepared) inspectHeader() {
	p.cliVersion = "unknown"
	if p.firstComplete {
		var record wireRecord
		if json.Unmarshal(trimLineEnding(p.first), &record) == nil {
			p.ordinal = record.Ordinal != nil
			if record.Type == "session_meta" {
				var meta sessionMeta
				if json.Unmarshal(record.Payload, &meta) == nil {
					p.sessionID = strings.TrimSpace(meta.ID)
					if strings.TrimSpace(meta.CLIVersion) != "" {
						p.cliVersion = strings.TrimSpace(meta.CLIVersion)
					}
					timestamp := strings.TrimSpace(meta.Timestamp)
					if timestamp == "" {
						timestamp = strings.TrimSpace(record.Timestamp)
					}
					if timestamp != "" {
						if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
							p.startedAt = &parsed
						} else {
							p.diagnostics = append(p.diagnostics, model.Diagnostic{Code: "codex.session.timestamp.invalid", Severity: model.SeverityWarning, Message: "The Codex session metadata timestamp is malformed."})
						}
					}
				}
			}
		}
	}
	if p.sessionID == "" {
		sum := sha256.Sum256([]byte(p.source.ID))
		p.sessionID = "codex_" + hex.EncodeToString(sum[:])
	}
	p.format = compositeFormat(p.ordinal, p.cliVersion)
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
	if remaining > 0 && len(p.first) > 0 {
		if int64(len(p.first)) > remaining {
			return fmt.Errorf("checkpoint offset %d splits the first JSONL record", target)
		}
		if err := p.writeVerified(p.first); err != nil {
			return err
		}
		remaining -= int64(len(p.first))
		p.firstUsed = true
	}
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
		source = io.MultiReader(bytes.NewReader(p.first), p.reader)
	}
	p.reader = bufio.NewReaderSize(source, readBuffer)
	p.first = nil
	p.firstUsed = true
	p.offset = 0
	p.seq = 0
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
			envelope, normalizeErr := p.envelope(line, start, p.seq, session)
			if normalizeErr != nil {
				return normalizeErr
			}
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
	if !p.firstUsed {
		p.firstUsed = true
		if p.firstComplete {
			return p.first, true, nil
		}
		return p.first, false, io.EOF
	}
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

func (p *prepared) envelope(line []byte, offset, sequence int64, session model.Session) (importer.RecordEnvelope, error) {
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
		envelope.Diagnostics = []model.Diagnostic{{Code: "codex.record.malformed", Severity: model.SeverityWarning, Message: "A complete Codex rollout record is malformed JSON and was retained.", RawRecordIDs: []model.RawRecordID{rawID}}}
		return envelope, nil
	}
	event, diagnostic, emit, err := p.normalize(record, ref, sequence, session.ID)
	if err != nil {
		return importer.RecordEnvelope{}, err
	}
	if emit {
		envelope.Events = []model.Event{event}
	}
	if diagnostic != nil {
		diagnostic.RawRecordIDs = []model.RawRecordID{rawID}
		if emit {
			diagnostic.EventIDs = []model.EventID{event.ID}
		}
		envelope.Diagnostics = []model.Diagnostic{*diagnostic}
	}
	return envelope, nil
}

func (p *prepared) normalize(record wireRecord, ref model.RawRecordRef, sequence int64, sessionID model.SessionID) (model.Event, *model.Diagnostic, bool, error) {
	if record.Type == "session_meta" {
		return model.Event{}, nil, false, nil
	}
	kind, summary, searchable, data, native, supported := normalizeRecord(record, p.ordinal, string(sessionID))
	if supported && data == nil {
		return model.Event{}, nil, false, nil
	}
	if !supported {
		label := record.Type
		if nested := nestedType(record.Payload); nested != "" {
			label += ":" + nested
		}
		kind, summary, data = model.EventKindUnknown, "Unsupported Codex record: "+label, model.UnknownData{OriginalKind: label}
		searchable = label
	}
	eventID, err := model.NewEventID(model.EventIDInput{Native: native, SourceID: ref.SourceID, RecordSequence: ref.RecordSequence, ByteRange: ref.ByteRange, RecordHash: ref.ContentHash})
	if err != nil {
		return model.Event{}, nil, false, err
	}
	event := model.Event{ID: eventID, SessionID: sessionID, Sequence: sequence, Kind: kind, Summary: summary, SearchableText: searchable, Data: data, RawRecord: ref}
	var diagnostic *model.Diagnostic
	if strings.TrimSpace(record.Timestamp) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, record.Timestamp)
		if err != nil {
			diagnostic = &model.Diagnostic{Code: "codex.timestamp.invalid", Severity: model.SeverityWarning, Message: "The rollout timestamp is malformed; source order was preserved."}
		} else {
			event.Timestamp = &parsed
		}
	}
	return event, diagnostic, true, nil
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
