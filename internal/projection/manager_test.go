package projection

import (
	"context"
	"testing"
)

func TestManagerBuildAvailable(t *testing.T) {
	builder := BuilderFunc(func(context.Context, BuildRequest) error { return nil })
	manager := &Manager{builders: map[Kind]Builder{
		KindSearch: builder,
	}}

	if !manager.BuildAvailable(KindSearch) {
		t.Fatal("BuildAvailable(search) = false, want true for registered builder")
	}
	if manager.BuildAvailable(KindFindings) {
		t.Fatal("BuildAvailable(findings) = true, want false for unregistered builder")
	}
}
