package opencode

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

type container struct {
	adapter  *Adapter
	source   importer.Source
	snapshot *snapshot
	schema   schemaInfo
	closed   bool
}

func (c *container) Children(ctx context.Context) ([]importer.PreparedChild, error) {
	rows, err := c.snapshot.tx.QueryContext(ctx, `SELECT id, title, time_created, time_updated FROM session ORDER BY time_created, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var children []importer.PreparedChild
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var nativeID string
		var title sql.NullString
		var created, updated any
		if err := rows.Scan(&nativeID, &title, &created, &updated); err != nil {
			return nil, err
		}
		selected, err := selectGeneration(ctx, c.snapshot.tx, c.schema, nativeID)
		if err != nil {
			return nil, fmt.Errorf("select OpenCode generation for session: %w", err)
		}
		childID := logicalSourceID(c.source.ID, nativeID)
		childSource := importer.Source{ID: childID, Hint: "opencode", LocalPath: c.source.LocalPath}
		meta := sessionMeta{nativeID: nativeID, title: title.String, created: created, updated: updated}
		children = append(children, importer.PreparedChild{Source: childSource, Prepared: &prepared{
			adapter: c.adapter, source: childSource, tx: c.snapshot.tx, meta: meta, selected: selected,
		}})
	}
	return children, rows.Err()
}

func (c *container) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.snapshot.close()
}

func logicalSourceID(containerID model.SourceID, nativeID string) model.SourceID {
	sum := sha256.Sum256([]byte("agentsession:opencode-logical-source:v1\x00" + string(containerID) + "\x00" + nativeID))
	return model.SourceID("opencode_src_" + hex.EncodeToString(sum[:]))
}

func canonicalSessionID(sourceID model.SourceID) model.SessionID {
	sum := sha256.Sum256([]byte("agentsession:opencode-session:v1\x00" + string(sourceID)))
	return model.SessionID("opencode_" + hex.EncodeToString(sum[:]))
}

type sessionMeta struct {
	nativeID string
	title    string
	created  any
	updated  any
}
