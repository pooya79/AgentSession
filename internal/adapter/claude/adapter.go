// Package claude imports Claude Code session JSONL files.
package claude

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
	CursorVersion        model.Version = "claude-code-jsonl-cursor-v1"
	FingerprintVersion   model.Version = "claude-code-jsonl-fingerprint-v1"

	formatBase  = "claude-code-jsonl-v1"
	readBuffer  = 32 << 10
	probeWindow = 8
	probeHeader = 64 << 10
)

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (*Adapter) Name() string           { return "claude" }
func (*Adapter) Version() model.Version { return AdapterVersion }

func (a *Adapter) Probe(ctx context.Context, source importer.Source) (importer.ProbeResult, error) {
	if err := source.Validate(); err != nil {
		return importer.ProbeResult{}, err
	}
	stream, err := source.OpenFrom(ctx, 0)
	if err != nil {
		return importer.ProbeResult{}, fmt.Errorf("open Claude session for probe: %w", err)
	}
	defer stream.Close()

	reader := bufio.NewReaderSize(stream, readBuffer)
	if header, _ := reader.Peek(len("SQLite format 3\x00")); bytes.Equal(header, []byte("SQLite format 3\x00")) {
		return importer.ProbeResult{Confidence: importer.ProbeUnsupported}, nil
	}
	recognized, valid, possibleMalformed := false, false, false
	cliVersion := "unknown"
	var diagnostics []model.Diagnostic
	for i := 0; i < probeWindow; i++ {
		if err := ctx.Err(); err != nil {
			return importer.ProbeResult{}, err
		}
		line, complete, readErr := readLine(reader)
		if len(line) == 0 && readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return importer.ProbeResult{}, fmt.Errorf("read Claude session probe: %w", readErr)
		}
		if !complete {
			possibleMalformed = possibleMalformed || looksClaudeLike(line)
			break
		}
		var record wireRecord
		if err := json.Unmarshal(trimLineEnding(line), &record); err != nil {
			diagnostics = append(diagnostics, model.Diagnostic{Code: "claude.probe.malformed", Severity: model.SeverityWarning, Message: "A complete JSONL record is malformed."})
			possibleMalformed = possibleMalformed || looksClaudeLike(line)
			continue
		}
		valid = true
		if knownTopLevel(record.Type) || record.SessionID != "" || hasJSONField(trimLineEnding(line), "isSidechain") {
			recognized = true
		}
		if value := strings.TrimSpace(record.Version); value != "" {
			cliVersion = value
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
	return importer.ProbeResult{Confidence: confidence, FormatVersion: compositeFormat(cliVersion), Diagnostics: diagnostics}, nil
}

func (a *Adapter) Prepare(ctx context.Context, source importer.Source) (importer.PreparedSource, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	stream, err := source.OpenFrom(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("open Claude session: %w", err)
	}
	reader := bufio.NewReaderSize(stream, readBuffer)
	replay, err := os.CreateTemp("", "agentsession-claude-probe-*")
	if err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("create Claude probe replay: %w", err)
	}
	view := &prepared{adapter: a, source: source, sourceStream: stream, replay: replay, cliVersion: "unknown"}
	for i := 0; i < probeWindow; i++ {
		if err := ctx.Err(); err != nil {
			_ = view.Close()
			return nil, err
		}
		line, header, complete, truncated, readErr := replayLine(reader, replay, probeHeader)
		if complete {
			view.inspectRecord(line, header, truncated)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				_ = view.Close()
				return nil, fmt.Errorf("inspect Claude session header: %w", readErr)
			}
			break
		}
	}
	if view.sessionID == "" {
		sum := sha256.Sum256([]byte(source.ID))
		view.sessionID = "claude_" + hex.EncodeToString(sum[:])
	}
	view.format = compositeFormat(view.cliVersion)
	if _, err := replay.Seek(0, io.SeekStart); err != nil {
		_ = view.Close()
		return nil, fmt.Errorf("rewind Claude probe replay: %w", err)
	}
	view.reader = bufio.NewReaderSize(io.MultiReader(replay, reader), readBuffer)
	return view, nil
}

type wireRecord struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	SessionID   string          `json:"sessionId"`
	Version     string          `json:"version"`
	Timestamp   string          `json:"timestamp"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
	Summary     string          `json:"summary"`
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
	reader       *bufio.Reader
	cliVersion   string
	sessionID    string
	format       model.Version

	offset    int64
	recordSeq int64
	eventSeq  int64
	digest    hash.Hash
	replay    *os.File
	spool     *os.File
	closed    bool
}

func (p *prepared) inspectRecord(line []byte, header probeRecordHeader, truncated bool) {
	var record wireRecord
	if !truncated {
		if json.Unmarshal(trimLineEnding(line), &record) != nil {
			return
		}
		if p.sessionID == "" {
			p.sessionID = strings.TrimSpace(record.SessionID)
		}
		if value := strings.TrimSpace(record.Version); value != "" {
			p.cliVersion = value
		}
		return
	}
	if p.sessionID == "" {
		p.sessionID = strings.TrimSpace(header.sessionID)
	}
	if value := strings.TrimSpace(header.version); value != "" {
		p.cliVersion = value
	}
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
	p.recordSeq = state.Checkpoint.RecordSequence + 1
	p.eventSeq = 0
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
	spool, err := os.CreateTemp("", "agentsession-claude-prefix-*")
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
			if _, err := p.digest.Write(buf[:n]); err != nil {
				return err
			}
			if _, err := p.spool.Write(buf[:n]); err != nil {
				return fmt.Errorf("spool verified prefix: %w", err)
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

func (p *prepared) Import(ctx context.Context, resume *importer.ImportCheckpoint, sink importer.ImportSink) error {
	if resume != nil && p.digest == nil {
		return fmt.Errorf("resume import requires prior verification")
	}
	if p.digest == nil {
		p.digest = sha256.New()
		p.recordSeq = 0
		p.eventSeq = 0
	}
	return p.stream(ctx, sink)
}

func (p *prepared) Reconcile(ctx context.Context, sink importer.ImportSink) error {
	if p.spool != nil {
		if _, err := p.spool.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind verified prefix: %w", err)
		}
		p.reader = bufio.NewReaderSize(io.MultiReader(p.spool, p.reader), readBuffer)
	}
	p.offset = 0
	p.recordSeq = 0
	p.eventSeq = 0
	p.digest = sha256.New()
	return p.stream(ctx, sink)
}

func (p *prepared) stream(ctx context.Context, sink importer.ImportSink) error {
	session := model.Session{ID: model.SessionID(p.sessionID), Import: model.ImportMetadata{
		SourceID: p.source.ID, AdapterName: p.adapter.Name(), AdapterVersion: p.adapter.Version(),
		FormatVersion: p.format, ModelVersion: ModelVersion, NormalizationVersion: NormalizationVersion,
	}}
	if err := sink.Begin(ctx, session); err != nil {
		return err
	}
	checkpoint, err := p.checkpoint(p.recordSeq - 1)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, complete, readErr := readLine(p.reader)
		if len(line) > 0 && complete {
			start := p.offset
			_, _ = p.digest.Write(line)
			p.offset += int64(len(line))
			envelope, normalizeErr := p.envelope(line, start, p.recordSeq, session)
			if normalizeErr != nil {
				return normalizeErr
			}
			envelope.Checkpoint, err = p.checkpoint(p.recordSeq)
			if err != nil {
				return err
			}
			if err := sink.Accept(ctx, envelope); err != nil {
				return err
			}
			checkpoint = envelope.Checkpoint
			p.recordSeq++
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return sink.Complete(ctx, session, checkpoint)
			}
			return fmt.Errorf("read Claude session record: %w", readErr)
		}
	}
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
		envelope.Diagnostics = []model.Diagnostic{{Code: "claude.record.malformed", Severity: model.SeverityWarning, Message: "A complete Claude Code record is malformed JSON and was retained.", RawRecordIDs: []model.RawRecordID{rawID}}}
		return envelope, nil
	}

	drafts := normalizeRecord(record)
	for ordinal, draft := range drafts {
		eventID, err := claudeEventID(record.UUID, p.sessionID, uint64(ordinal), len(drafts), ref)
		if err != nil {
			return importer.RecordEnvelope{}, err
		}
		event := model.Event{ID: eventID, SessionID: session.ID, Sequence: p.eventSeq, Kind: draft.kind, Summary: draft.summary, SearchableText: draft.searchable, Data: draft.data, RawRecord: ref}
		p.eventSeq++
		envelope.Events = append(envelope.Events, event)
	}
	if value := strings.TrimSpace(record.Timestamp); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			diagnostic := model.Diagnostic{Code: "claude.timestamp.invalid", Severity: model.SeverityWarning, Message: "The Claude record timestamp is malformed; source order was preserved.", RawRecordIDs: []model.RawRecordID{rawID}}
			for _, event := range envelope.Events {
				diagnostic.EventIDs = append(diagnostic.EventIDs, event.ID)
			}
			envelope.Diagnostics = append(envelope.Diagnostics, diagnostic)
		} else {
			for i := range envelope.Events {
				envelope.Events[i].Timestamp = &parsed
			}
		}
	}
	return envelope, nil
}

func claudeEventID(uuid, sessionID string, ordinal uint64, count int, ref model.RawRecordRef) (model.EventID, error) {
	if uuid = strings.TrimSpace(uuid); uuid != "" {
		if count > 1 {
			uuid = fmt.Sprintf("%s:event:%d", uuid, ordinal)
		}
		return model.NewEventID(model.EventIDInput{Native: &model.NativeEventIdentity{Scope: model.NativeEventIDSession, SessionID: sessionID, EventID: uuid}})
	}
	return model.NewEventID(model.EventIDInput{SourceID: ref.SourceID, RecordSequence: ref.RecordSequence, ByteRange: ref.ByteRange, RecordHash: ref.ContentHash, EventOrdinal: ordinal})
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

// replayLine copies a record to replay storage as it is read and retains only a
// bounded prefix for header inspection. The full record is reconstructed from
// replay storage by the normal import path instead of being held by Prepare.
func replayLine(reader *bufio.Reader, replay io.Writer, captureLimit int) ([]byte, probeRecordHeader, bool, bool, error) {
	sample := make([]byte, 0, min(captureLimit, readBuffer))
	inspector := probeHeaderInspector{expectKey: false}
	truncated := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if _, writeErr := replay.Write(fragment); writeErr != nil {
				return nil, probeRecordHeader{}, false, truncated, fmt.Errorf("spool Claude probe record: %w", writeErr)
			}
			inspector.write(fragment)
			remaining := captureLimit - len(sample)
			captured := 0
			if remaining > 0 {
				captured = len(fragment)
				if captured > remaining {
					captured = remaining
				}
				sample = append(sample, fragment[:captured]...)
			}
			truncated = truncated || captured < len(fragment)
		}
		if err == nil {
			return sample, inspector.header, true, truncated, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return sample, inspector.header, false, truncated, io.EOF
		}
		return sample, inspector.header, false, truncated, err
	}
}

type probeRecordHeader struct {
	sessionID string
	version   string
}

type probeHeaderInspector struct {
	header    probeRecordHeader
	depth     int
	inString  bool
	escaped   bool
	expectKey bool
	pending   string
	capture   string
	value     []byte
	overflow  bool
}

func (p *probeHeaderInspector) write(data []byte) {
	const maxHeaderValue = 4 << 10
	for _, b := range data {
		if p.inString {
			if p.capture != "" && !p.overflow {
				if len(p.value) == maxHeaderValue {
					p.overflow = true
				} else {
					p.value = append(p.value, b)
				}
			}
			if p.escaped {
				p.escaped = false
				continue
			}
			if b == '\\' {
				p.escaped = true
				continue
			}
			if b == '"' {
				p.finishString()
			}
			continue
		}

		switch b {
		case '{':
			p.depth++
			if p.depth == 1 {
				p.expectKey = true
			} else if p.depth == 2 {
				p.pending = ""
			}
		case '[':
			p.depth++
			if p.depth == 2 {
				p.pending = ""
			}
		case '}', ']':
			p.depth--
		case '"':
			p.inString = true
			p.overflow = false
			p.value = p.value[:0]
			switch {
			case p.depth == 1 && p.expectKey:
				p.capture = "key"
			case p.depth == 1 && p.pending != "":
				p.capture = p.pending
			default:
				p.capture = ""
			}
			if p.capture != "" {
				p.value = append(p.value, '"')
			}
		case ',':
			if p.depth == 1 {
				p.expectKey = true
				p.pending = ""
			}
		case ' ', '\t', '\r', '\n', ':':
		default:
			if p.depth == 1 && p.pending != "" {
				p.pending = ""
			}
		}
	}
}

func (p *probeHeaderInspector) finishString() {
	p.inString = false
	if p.capture == "" || p.overflow {
		p.capture = ""
		return
	}
	var value string
	if json.Unmarshal(p.value, &value) != nil {
		p.capture = ""
		return
	}
	switch p.capture {
	case "key":
		p.expectKey = false
		if value == "sessionId" || value == "version" {
			p.pending = value
		} else {
			p.pending = ""
		}
	case "sessionId":
		p.header.sessionID = value
		p.pending = ""
	case "version":
		p.header.version = value
		p.pending = ""
	}
	p.capture = ""
}

func trimLineEnding(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	return bytes.TrimSuffix(line, []byte{'\r'})
}

func compositeFormat(cli string) model.Version {
	cli = strings.TrimSpace(cli)
	if cli == "" {
		cli = "unknown"
	}
	return model.Version(formatBase + "+cli-" + cli)
}

func knownTopLevel(kind string) bool {
	switch kind {
	case "user", "assistant", "system", "summary", "file-history-snapshot", "progress", "queue-operation":
		return true
	default:
		return false
	}
}

func hasJSONField(data []byte, name string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	_, ok := fields[name]
	return ok
}

func looksClaudeLike(line []byte) bool {
	for _, label := range []string{"user", "assistant", "summary", "file-history-snapshot"} {
		if bytes.Contains(line, []byte(`"type":"`+label)) || bytes.Contains(line, []byte(`"type": "`+label)) {
			return true
		}
	}
	return bytes.Contains(line, []byte(`"sessionId"`)) || bytes.Contains(line, []byte(`"isSidechain"`))
}
