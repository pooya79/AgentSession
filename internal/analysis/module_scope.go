package analysis

import (
	"context"
	"path"
	"strings"

	"github.com/pooya79/AgentSession/internal/model"
)

func containsDirectory(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
}

// A confirmed go.mod effect is a recorded structural marker, unlike a cwd or
// a patch request. It establishes declared module scope, not valid contents,
// dependency relationships or actual participation. go.sum alone is not a marker.
func configurationScope(ctx context.Context, session model.SessionID, reader ChangeReader, c ChangeEvidence, file string) (bool, []model.EventID, error) {
	root := path.Dir(file)
	if !ordinaryGoDirectory(root) || c.PreviousPath != "" || c.Operation == model.FileMutationRename || c.Operation == model.FileMutationDelete {
		return false, nil, nil
	}
	established, ambiguous := false, false
	var links []model.EventID
	err := reader.WalkChanges(ctx, session, func(marker ChangeEvidence) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if marker.Status != model.ChangeConfirmed {
			return nil
		}
		for _, value := range []string{marker.Path, marker.PreviousPath} {
			file, ok := establishedPath(value, marker.Directory)
			if !ok {
				continue
			}
			dir := path.Dir(file)
			switch path.Base(file) {
			case "go.mod":
				if dir == root {
					if marker.Position <= c.Position {
						established = marker.Operation == model.FileMutationCreate || marker.Operation == model.FileMutationUpdate
					} else if marker.Operation == model.FileMutationDelete || marker.Operation == model.FileMutationRename {
						ambiguous = true
					}
				} else if containsDirectory(root, dir) || containsDirectory(dir, root) {
					ambiguous = true
				}
			case "go.work", "go.work.sum":
				if containsDirectory(dir, root) || containsDirectory(root, dir) {
					ambiguous = true
				}
			default:
				continue
			}
			if (dir == root || containsDirectory(root, dir) || containsDirectory(dir, root)) && len(links) < MaxEvidenceReferences {
				links = append(links, marker.EventID)
			}
		}
		return nil
	})
	return established && !ambiguous, links, err
}

// Recursive Go targets do not establish reach across a recorded nested module.
// Workspace membership, removal and relocation need richer structural evidence;
// keep that relationship uncertain rather than guessing package participation.
func crossesRecordedModule(ctx context.Context, session model.SessionID, reader ChangeReader, target, directory string, start int64) (model.EventID, error) {
	var boundary model.EventID
	err := reader.WalkChanges(ctx, session, func(marker ChangeEvidence) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if marker.Status != model.ChangeConfirmed || marker.Position > start {
			return nil
		}
		for _, value := range []string{marker.Path, marker.PreviousPath} {
			file, ok := establishedPath(value, marker.Directory)
			if !ok || path.Base(file) != "go.mod" {
				continue
			}
			root := path.Dir(file)
			if root != target && containsDirectory(target, root) && containsDirectory(root, directory) && boundary == "" {
				boundary = marker.EventID
			}
		}
		return nil
	})
	return boundary, err
}
