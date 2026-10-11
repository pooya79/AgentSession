package claude

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

type captureSink struct {
	session     model.Session
	records     []importer.RecordEnvelope
	checkpoint  importer.ImportCheckpoint
	acceptError error
	onAccept    func(importer.RecordEnvelope)
}

func (s *captureSink) Begin(_ context.Context, session model.Session) error {
	s.session = session
	return nil
}

func (s *captureSink) Accept(_ context.Context, envelope importer.RecordEnvelope) error {
	if s.acceptError != nil {
		return s.acceptError
	}
	if s.onAccept != nil {
		s.onAccept(envelope)
	}
	s.records = append(s.records, envelope)
	return nil
}

func (s *captureSink) Complete(_ context.Context, session model.Session, checkpoint importer.ImportCheckpoint) error {
	s.session = session
	s.checkpoint = checkpoint
	return nil
}

func fixtureSource(t *testing.T, name string) importer.Source {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return bytesSource("fixture:"+name, data)
}

func bytesSource(id string, data []byte) importer.Source {
	data = append([]byte(nil), data...)
	return importer.Source{ID: model.SourceID(id), Size: int64(len(data)), OpenAt: func(_ context.Context, offset int64) (io.ReadCloser, error) {
		if offset < 0 || offset > int64(len(data)) {
			return nil, errors.New("offset out of range")
		}
		return io.NopCloser(bytes.NewReader(data[offset:])), nil
	}}
}

func importSource(t *testing.T, source importer.Source, resume *importer.ImportCheckpoint, state *importer.SourceState) (*captureSink, importer.SourceChange) {
	t.Helper()
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	change := importer.SourceNew
	if state != nil {
		change, err = view.Verify(context.Background(), *state)
		if err != nil {
			t.Fatal(err)
		}
	}
	sink := &captureSink{}
	if err := view.Import(context.Background(), resume, sink); err != nil {
		t.Fatal(err)
	}
	return sink, change
}

func sourceState(sink *captureSink) importer.SourceState {
	var last *int64
	all := events(sink.records)
	if len(all) > 0 {
		value := all[len(all)-1].Sequence
		last = &value
	}
	return importer.SourceState{SessionID: sink.session.ID, Import: sink.session.Import, Session: sink.session, Checkpoint: sink.checkpoint, LastEventSequence: last}
}

func events(records []importer.RecordEnvelope) []model.Event {
	var result []model.Event
	for _, record := range records {
		result = append(result, record.Events...)
	}
	return result
}

type trackingReadCloser struct {
	reader    *bytes.Reader
	readBytes int
	failAfter int
	fail      error
}

func (r *trackingReadCloser) Read(p []byte) (int, error) {
	if r.fail != nil && r.readBytes >= r.failAfter {
		return 0, r.fail
	}
	n, err := r.reader.Read(p)
	r.readBytes += n
	return n, err
}

func (*trackingReadCloser) Close() error { return nil }
