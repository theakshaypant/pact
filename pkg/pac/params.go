package pac

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	packeys "github.com/openshift-pipelines/pipelines-as-code/pkg/apis/pipelinesascode/keys"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"github.com/theakshaypant/pact/pkg/event"
)

// Params are user-supplied custom parameter values, the local stand-in
// for PAC's Repository CR params: they substitute {{ name }} placeholders
// while parsing the .tekton manifests and are merged into every matched
// PipelineRun's spec.params at resolve time.
type Params map[string]string

// ErrUnknownParams wraps the typo check: a supplied param that no matched
// pipeline declares or references.
var ErrUnknownParams = errors.New("unknown params")

// paramNameRE is the grammar a custom param name must follow: simple
// identifiers only.
var paramNameRE = regexp.MustCompile(`^[_a-zA-Z][_a-zA-Z0-9-]*$`)

// reservedParamPrefixes are PAC template key prefixes routed through CEL
// lookups rather than the dictionary, with no trailing separator
// required: a custom param starting with one would silently never
// substitute.
var reservedParamPrefixes = []string{"body", "headers", "files"}

// NewParams validates a custom param map: names must be simple
// identifiers that neither start with a reserved PAC prefix nor shadow a
// built-in template key (revision, repo_url, ...) — the substituter would
// silently ignore both. An empty map returns nil params.
func NewParams(m map[string]string) (Params, error) {
	if len(m) == 0 {
		return nil, nil
	}
	builtins := templateDict(&event.Event{}, nil)
	for _, name := range slices.Sorted(maps.Keys(m)) {
		if !paramNameRE.MatchString(name) {
			return nil, fmt.Errorf("invalid param name %q", name)
		}
		if _, shadows := builtins[name]; shadows {
			return nil, fmt.Errorf("param %q shadows a built-in template parameter", name)
		}
		if prefix := reservedParamPrefix(name); prefix != "" {
			return nil, fmt.Errorf("param %q starts with the reserved prefix %q (PAC routes such keys through CEL, so it would never substitute)",
				name, prefix)
		}
	}
	return Params(m), nil
}

// reservedParamPrefix returns the reserved prefix name starts with, or "".
func reservedParamPrefix(name string) string {
	for _, prefix := range reservedParamPrefixes {
		if strings.HasPrefix(name, prefix) {
			return prefix
		}
	}
	return ""
}

// applyParams upserts the params into every PipelineRun's spec.params.
// Tekton accepts PipelineRun params the pipeline does not declare, so
// appending is safe.
func applyParams(prs []*tektonv1.PipelineRun, p Params) {
	for _, pr := range prs {
		for _, name := range slices.Sorted(maps.Keys(p)) {
			pr.Spec.Params = upsertParam(pr.Spec.Params, tektonv1.Param{
				Name:  name,
				Value: tektonv1.ParamValue{Type: tektonv1.ParamTypeString, StringVal: p[name]},
			})
		}
	}
}

// upsertParam replaces a same-named param in place or appends it (the
// same upsert shape as the runner's workspace binding).
func upsertParam(params []tektonv1.Param, p tektonv1.Param) []tektonv1.Param {
	if i := slices.IndexFunc(params, func(existing tektonv1.Param) bool {
		return existing.Name == p.Name
	}); i >= 0 {
		params[i] = p
		return params
	}
	return append(params, p)
}

// usedParamNames collects the param names the matched pipelines use:
// names declared by a resolved pipelineSpec, names already set on a
// PipelineRun, and {{ name }} placeholders in the raw text of the files
// that contributed a match (placeholders are gone once substituted).
func usedParamNames(prs []*tektonv1.PipelineRun, rawMatched []string) map[string]bool {
	used := map[string]bool{}
	for _, pr := range prs {
		if pr.Spec.PipelineSpec != nil {
			for _, p := range pr.Spec.PipelineSpec.Params {
				used[p.Name] = true
			}
		}
		for _, p := range pr.Spec.Params {
			used[p.Name] = true
		}
	}
	for _, content := range rawMatched {
		for _, m := range packeys.ParamsRe.FindAllStringSubmatch(content, -1) {
			if name := strings.TrimSpace(m[1]); paramNameRE.MatchString(name) {
				used[name] = true
			}
		}
	}
	return used
}

// unknown returns the sorted names in p that used does not contain.
func (p Params) unknown(used map[string]bool) []string {
	var unknown []string
	for _, name := range slices.Sorted(maps.Keys(p)) {
		if !used[name] {
			unknown = append(unknown, name)
		}
	}
	return unknown
}
