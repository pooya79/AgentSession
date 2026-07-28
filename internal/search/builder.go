package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
)

const buildBatchSize = 256

type Document struct {
	EventID           model.EventID
	SessionID         model.SessionID
	Sequence          int64
	Timestamp         string
	Kind              model.EventKind
	Summary           string
	Content           string
	ToolName          string
	CommandText       string
	Files             []string
	ProjectionVersion string
	CanonicalRevision int64
}

// ProjectionWriter owns search staging and atomic publication.
type ProjectionWriter interface {
	StageSearchDocuments(context.Context, string, []Document) error
	PublishSearchDocuments(context.Context, string, model.SessionID, string, int64) error
	CleanupSearchStage(context.Context, string) error
}

type Builder struct{ writer ProjectionWriter }

func NewBuilder(writer ProjectionWriter) (*Builder, error) {
	if writer == nil {
		return nil, errors.New("search builder: projection writer is required")
	}
	return &Builder{writer: writer}, nil
}

func (b *Builder) Build(ctx context.Context, request projection.BuildRequest) (err error) {
	if request.Reader == nil || request.BuildToken == "" || request.Version == "" {
		return errors.New("search builder: incomplete build request")
	}
	defer func() {
		// Staging is never queryable. Cleanup failure must not turn a completed
		// atomic publication into a failed lifecycle state.
		_ = b.writer.CleanupSearchStage(context.WithoutCancel(ctx), request.BuildToken)
	}()
	var after *int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		events, err := request.Reader.EventPage(ctx, request.SessionID, after, buildBatchSize)
		if err != nil {
			return fmt.Errorf("search builder: read canonical event page: %w", err)
		}
		if len(events) == 0 {
			break
		}
		documents := make([]Document, 0, len(events))
		for _, event := range events {
			documents = append(documents, documentFromEvent(event, request.Version, request.CanonicalRevision))
		}
		if err := b.writer.StageSearchDocuments(ctx, request.BuildToken, documents); err != nil {
			return fmt.Errorf("search builder: stage document batch: %w", err)
		}
		last := events[len(events)-1].Sequence
		after = &last
	}
	if err := b.writer.PublishSearchDocuments(ctx, request.BuildToken, request.SessionID, request.Version, request.CanonicalRevision); err != nil {
		return fmt.Errorf("search builder: publish documents: %w", err)
	}
	return nil
}

func documentFromEvent(event model.Event, version string, revision int64) Document {
	document := Document{
		EventID: event.ID, SessionID: event.SessionID, Sequence: event.Sequence,
		Kind: event.Kind, Summary: safePrefix(event.Summary, 2*1024),
		ProjectionVersion: version, CanonicalRevision: revision,
	}
	if event.Timestamp != nil {
		document.Timestamp = event.Timestamp.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	text := []string{document.Summary}
	addText := func(value string, maximum int) {
		if eligibleText(value, maximum) && value != "" {
			text = append(text, value)
		}
	}
	addFile := func(value string) {
		if eligibleText(value, 4*1024) && value != "" {
			normalized := normalizePath(value)
			if normalized != "" && normalized != "." {
				document.Files = append(document.Files, normalized)
			}
		}
	}
	switch data := event.Data.(type) {
	case model.MessageData:
		addText(data.Text, 64*1024)
	case model.ToolCallData:
		if eligibleText(data.ToolName, 512) {
			document.ToolName = strings.ToLower(data.ToolName)
		}
		addText(data.Input, 64*1024)
	case model.ToolResultData:
		if eligibleText(data.ToolName, 512) {
			document.ToolName = strings.ToLower(data.ToolName)
		}
		addText(data.Output, 64*1024)
	case model.CommandData:
		if eligibleText(data.Command, 16*1024) {
			document.CommandText = strings.ToLower(data.Command)
			addText(data.Command, 16*1024)
		}
		addText(data.Output, 64*1024)
	case model.FileReadData:
		addFile(data.Path)
	case model.FileMutationData:
		addFile(data.Path)
		addFile(data.PreviousPath)
	case model.PatchData:
		addText(data.Text, 64*1024)
		for _, value := range data.Paths {
			addFile(value)
		}
	case model.ErrorData:
		addText(data.Message, 64*1024)
	case model.SummaryData:
		addText(data.Text, 64*1024)
	}
	document.Content = strings.Join(text, "\n")
	return document
}

func eligibleText(value string, maximum int) bool {
	return len(value) <= maximum && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0
}

func safePrefix(value string, maximum int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
