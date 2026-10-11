package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

type prepared struct {
	evidence     evidenceState
	adapter      *Adapter
	source       importer.Source
	sourceStream io.ReadCloser
	reader       *bufio.Reader
	cliVersion   string
	sessionID    string
	agentID      string
	format       model.Version

	offset    int64
	recordSeq int64
	eventSeq  int64
	digest    hash.Hash
	replay    *os.File
	spool     *os.File
	closed    bool
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
	p.evidence = evidenceState{}
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
