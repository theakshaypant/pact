package pac

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/openshift-pipelines/pipelines-as-code/pkg/events"
	pacmatcher "github.com/openshift-pipelines/pipelines-as-code/pkg/matcher"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/params"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/params/info"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/provider"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/resolve"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/templates"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"github.com/theakshaypant/pact/pkg/event"
)

// noMatchMarker is the stable prefix of PAC's "nothing matched" error. The
// coupling is pinned by TestIsNoMatch so a PAC upgrade that changes the
// wording breaks loudly.
const noMatchMarker = "cannot match the event to any pipelineruns"

// isNoMatch reports whether err is PAC's "nothing matched" signal rather
// than a real failure.
func isNoMatch(err error) bool {
	return err != nil && strings.Contains(err.Error(), noMatchMarker)
}

// evaluation is the result of the shared load-parse-match pipeline behind
// Match and Resolve.
type evaluation struct {
	perFile    map[string]resolve.TektonTypes // hydrated types keyed by file
	matches    []Match                        // sorted by file name
	matchedPRs []*tektonv1.PipelineRun        // the matched PipelineRun objects
	files      []pipelineFile                 // the raw files, sorted by path
}

// evaluate loads the tekton directory, hydrates and parses each file, and
// runs PAC's annotation matcher per file. A file where nothing matched is
// not an error; an evaluation with zero matches is a valid result.
func (e *engine) evaluate(ctx context.Context, tektonDir string, ev *event.Event) (*evaluation, error) {
	files, err := loadTektonDir(tektonDir)
	if err != nil {
		return nil, err
	}
	perFile, err := e.parseFiles(ctx, files, ev)
	if err != nil {
		return nil, err
	}
	matches, matchedPRs, err := e.matchFiles(ctx, perFile, ev)
	if err != nil {
		return nil, err
	}
	return &evaluation{perFile: perFile, matches: matches, matchedPRs: matchedPRs, files: files}, nil
}

// matchedFileContent returns the raw content of the files that
// contributed a match, in sorted file order.
func (e *evaluation) matchedFileContent() []string {
	matched := map[string]bool{}
	for _, m := range e.matches {
		matched[m.File] = true
	}
	var out []string
	for _, f := range e.files {
		if matched[f.Name] {
			out = append(out, f.Content)
		}
	}
	return out
}

// Match returns the .tekton PipelineRuns matching the event, sorted by file.
func (e *engine) Match(ctx context.Context, tektonDir string, ev *event.Event) ([]Match, error) {
	res, err := e.evaluate(ctx, tektonDir, ev)
	if err != nil {
		return nil, fmt.Errorf("matching pipelines: %w", err)
	}
	return res.matches, nil
}

// parseFiles hydrates (template-substitutes) and parses each file,
// returning per-file TektonTypes so matches keep their file attribution.
// Files are keyed by name; iteration order is the sorted file order.
func (e *engine) parseFiles(ctx context.Context, files []pipelineFile, ev *event.Event) (map[string]resolve.TektonTypes, error) {
	dict := templateDict(ev, e.params)
	filesNS := filesMap(toPACFiles(ev.ChangedFiles))
	perFile := make(map[string]resolve.TektonTypes, len(files))
	for _, f := range files {
		hydrated := templates.ReplacePlaceHoldersVariables(f.Content, dict, ev.Payload, ev.Headers, filesNS)
		types, err := resolve.ReadTektonTypes(ctx, e.log, hydrated)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", f.Name, err)
		}
		perFile[f.Name] = types
	}
	return perFile, nil
}

// matchFiles runs PAC's annotation matcher once per file. A no-match result
// for a file is not an error; real errors (invalid CEL, bad annotations)
// are wrapped with the file name. It returns the matches plus the matched
// PipelineRun objects for resolve to build on.
func (e *engine) matchFiles(ctx context.Context, perFile map[string]resolve.TektonTypes, ev *event.Event) ([]Match, []*tektonv1.PipelineRun, error) {
	emitter := events.NewEventEmitter(nil, e.log) // a nil emitter would panic on warnings
	provider := newLocalProvider(ev.ChangedFiles)

	var matches []Match
	var matchedPRs []*tektonv1.PipelineRun
	for _, name := range sortedFileNames(perFile) {
		types := perFile[name]
		if len(types.PipelineRuns) == 0 {
			continue
		}
		matched, err := e.callMatcher(ctx, types.PipelineRuns, provider, emitter, toInfoEvent(ev))
		if err != nil && len(matched) == 0 && isNoMatch(err) {
			continue // nothing in this file matched
		}
		if err != nil {
			return nil, nil, fmt.Errorf("matching %s: %w", name, err)
		}
		for _, m := range matched {
			matches = append(matches, Match{File: name, PipelineRun: pipelineRunName(m.PipelineRun)})
			matchedPRs = append(matchedPRs, m.PipelineRun)
		}
	}
	return matches, matchedPRs, nil
}

// callMatcher invokes MatchPipelinerunByAnnotation, converting a panic from
// an unexpected provider method call into an error.
func (e *engine) callMatcher(ctx context.Context, pruns []*tektonv1.PipelineRun, prov provider.Interface, emitter *events.EventEmitter, ie *info.Event) (matched []pacmatcher.Match, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("PAC matcher panicked (unexpected provider call on the local stub): %v", r)
		}
	}()
	matched, _, err = pacmatcher.MatchPipelinerunByAnnotation(ctx, e.log, pruns, &params.Run{}, ie, prov, emitter, nil, false)
	return
}

// pipelineRunName returns the name or generateName of a PipelineRun.
func pipelineRunName(pr *tektonv1.PipelineRun) string {
	if name := pr.GetName(); name != "" {
		return name
	}
	return pr.GetGenerateName()
}

// sortedFileNames returns the per-file map keys in sorted order.
func sortedFileNames(perFile map[string]resolve.TektonTypes) []string {
	names := make([]string, 0, len(perFile))
	for name := range perFile {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
