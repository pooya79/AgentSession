package codex

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
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

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
