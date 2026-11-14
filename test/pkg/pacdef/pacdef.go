// Package pacdef builds the PipelineRun and Task manifests pact's test
// suites run against. Fixtures are Go values instead of YAML files so
// the e2e and unit suites share one definition per scenario instead of
// two drifting copies. Builders render typed API objects to
// hand-written-equivalent YAML: the marshal noise of the typed structs
// (status, creationTimestamp, taskRunTemplate, ...) is pruned before
// output.
package pacdef

import "strings"

// Manifest renders to one YAML document.
type Manifest interface {
	// YAML returns the manifest as a single YAML document, newline
	// terminated.
	YAML() string
}

// Stream joins documents into one multi-document YAML stream, the
// format .tekton files and resolve output use.
func Stream(docs ...Manifest) Manifest {
	return stream(docs)
}

type stream []Manifest

func (s stream) YAML() string {
	var b strings.Builder
	for i, doc := range s {
		if i > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(doc.YAML())
	}
	return b.String()
}

// bracket renders a PAC list annotation value: [a, b].
func bracket(values []string) string {
	return "[" + strings.Join(values, ", ") + "]"
}
