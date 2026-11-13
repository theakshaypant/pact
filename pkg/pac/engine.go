// Package pac drives Pipelines-as-Code matching and resolution entirely
// client-side. All imports of the pipelines-as-code library are confined to
// this package; the rest of pact only sees pact-owned types.
package pac

import (
	"context"

	"github.com/theakshaypant/pact/pkg/event"
	"go.uber.org/zap"
)

// Match identifies a PipelineRun that matches the simulated event.
type Match struct {
	// File is the path of the .tekton file relative to the tekton dir.
	File string
	// PipelineRun is the metadata.name or generateName of the match.
	PipelineRun string
}

// Engine matches and resolves Pipelines-as-Code definitions.
type Engine interface {
	// Match returns the .tekton PipelineRuns that match the event, sorted
	// by file name. An empty result with a nil error means nothing
	// matched.
	Match(ctx context.Context, tektonDir string, ev *event.Event) ([]Match, error)

	// Resolve hydrates the PipelineRuns that match the event into
	// self-contained multi-document YAML with referenced local tasks
	// inlined. Remote task references are not fetched (offline mode).
	Resolve(ctx context.Context, tektonDir string, ev *event.Event) ([]byte, error)
}

type engine struct {
	log    *zap.SugaredLogger
	params Params
}

// Option configures an Engine.
type Option func(*engine)

// WithParams supplies custom param values to the engine: they substitute
// {{ name }} placeholders while parsing the .tekton manifests and are
// merged into every matched PipelineRun's spec.params at resolve time.
func WithParams(p Params) Option {
	return func(e *engine) { e.params = p }
}

// New creates an Engine. A nil logger is replaced with a no-op logger.
func New(log *zap.SugaredLogger, opts ...Option) Engine {
	if log == nil {
		log = zap.NewNop().Sugar()
	}
	e := &engine{log: log}
	for _, opt := range opts {
		opt(e)
	}
	return e
}
