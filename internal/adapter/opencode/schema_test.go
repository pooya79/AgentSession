package opencode

import (
	"context"
	"database/sql"
	"testing"
)

func TestGenerationSelectionUsesOneAuthoritativeTimeline(t *testing.T) {
	ctx := context.Background()
	view, err := New().PrepareContainer(ctx, sourceFor(createNamedFixture(t, "coexisting_generations.sql")))
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	children, err := view.Children(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("children = %d, want 2", len(children))
	}
	want := []generation{generationEvent, generationMessage}
	for i, child := range children {
		prepared := child.Prepared.(*prepared)
		if prepared.selected.generation != want[i] {
			t.Fatalf("child %d generation = %q, want %q", i, prepared.selected.generation, want[i])
		}
		var tables []string
		if err := prepared.eachRecord(ctx, func(record logicalRecord) error {
			tables = append(tables, record.table)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(tables) < 2 {
			t.Fatalf("child %d tables = %v, want session and selected-generation records", i, tables)
		}
		if tables[0] != "session" {
			t.Fatalf("child %d first table = %q, want session: %v", i, tables[0], tables)
		}
		for _, table := range tables[1:] {
			if i == 0 && table != "event" {
				t.Fatalf("durable child retained cross-generation table %q: %v", table, tables)
			}
			if i == 1 && table != "session_message" {
				t.Fatalf("fallback child retained cross-generation table %q: %v", table, tables)
			}
		}
	}
}

func TestIncompleteDurableWithoutFallbackIsSelectedAndDiagnosed(t *testing.T) {
	ctx := context.Background()
	path := createNamedFixture(t, "durable_events_v1.sql")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE event_sequence SET seq = 99 WHERE aggregate_id = 'ev_main'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	view, err := New().PrepareContainer(ctx, sourceFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	children, err := view.Children(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prepared := children[0].Prepared.(*prepared)
	if prepared.selected.generation != generationEvent || !prepared.selected.durableIncomplete {
		t.Fatalf("selection = %#v", prepared.selected)
	}
	session := prepared.session()
	if len(session.Diagnostics) != 1 || session.Diagnostics[0].Code != "opencode.generation.durable.incomplete" {
		t.Fatalf("session diagnostics = %#v", session.Diagnostics)
	}
}
