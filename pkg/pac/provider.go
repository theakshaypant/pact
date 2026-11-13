package pac

import (
	"context"

	"github.com/openshift-pipelines/pipelines-as-code/pkg/changedfiles"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/params/info"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/provider"
	"github.com/theakshaypant/pact/pkg/git"
)

// toPACFiles is the single place pact's classified changed files are
// converted to PAC's type; both the matcher and the template engine consume
// the result.
func toPACFiles(files git.ChangedFiles) changedfiles.ChangedFiles {
	return changedfiles.ChangedFiles{
		All:      files.All,
		Added:    files.Added,
		Deleted:  files.Deleted,
		Modified: files.Modified,
		Renamed:  files.Renamed,
	}
}

// filesMap adapts the classified files for PAC's template engine, whose
// files namespace is a map[string]any.
func filesMap(files changedfiles.ChangedFiles) map[string]any {
	return map[string]any{
		"all":      files.All,
		"added":    files.Added,
		"deleted":  files.Deleted,
		"modified": files.Modified,
		"renamed":  files.Renamed,
	}
}

// localProvider supplies the locally computed changed files to PAC's
// matcher and resolver, standing in for a real forge.
//
// Every provider method other than GetFiles would panic on the nil embedded
// interface. The code paths pact exercises (offline resolve, matching with
// reportErrors=false and no Repository CRD) only ever call GetFiles; the
// matcher invocation is additionally guarded by a recover in matchFiles so
// an unexpected call surfaces as an error instead of a crash.
type localProvider struct {
	provider.Interface
	files changedfiles.ChangedFiles
}

func newLocalProvider(files git.ChangedFiles) *localProvider {
	return &localProvider{files: toPACFiles(files)}
}

// GetFiles returns the locally computed changed files.
func (p *localProvider) GetFiles(_ context.Context, _ *info.Event) (changedfiles.ChangedFiles, error) {
	files := p.files
	files.RemoveDuplicates()
	return files, nil
}
