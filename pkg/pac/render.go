package pac

import (
	"bytes"
	"fmt"

	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	v3yaml "go.yaml.in/yaml/v3"
	k8syaml "sigs.k8s.io/yaml"
)

// noiseKeys are mapping keys whose null or empty-mapping values are API
// defaulting noise rather than meaningful content:
//   - status, taskRunTemplate, creationTimestamp: empty k8s object fields
//     that always marshal on freshly created objects
//   - metadata, spec, computeResources: empty structs on embedded task and
//     step types (a non-empty value is always kept)
var noiseKeys = map[string]bool{
	"status":            true,
	"taskRunTemplate":   true,
	"creationTimestamp": true,
	"metadata":          true,
	"computeResources":  true,
	"spec":              true,
}

// renderPipelineRuns marshals resolved PipelineRuns into a multi-document
// YAML stream.
func renderPipelineRuns(prs []*tektonv1.PipelineRun) ([]byte, error) {
	var sb bytes.Buffer
	for _, pr := range prs {
		pr.APIVersion = tektonv1.SchemeGroupVersion.String()
		pr.Kind = "PipelineRun"
		pr.SetNamespace("")
		doc, err := k8syaml.Marshal(pr)
		if err != nil {
			return nil, fmt.Errorf("marshaling pipelinerun %s: %w", pipelineRunName(pr), err)
		}
		clean, err := stripNoise(doc)
		if err != nil {
			return nil, fmt.Errorf("cleaning pipelinerun %s: %w", pipelineRunName(pr), err)
		}
		sb.WriteString("---\n")
		sb.Write(clean)
		sb.WriteString("\n")
	}
	return sb.Bytes(), nil
}

// stripNoise removes entries with noise keys and null or empty-mapping
// values from a rendered YAML document. It operates on the node tree rather
// than the text so that adjacent noise lines and noise inside sequence items
// (e.g. "- computeResources: {}") are all removed.
func stripNoise(doc []byte) ([]byte, error) {
	var root v3yaml.Node
	if err := v3yaml.Unmarshal(doc, &root); err != nil {
		return nil, fmt.Errorf("parsing rendered yaml: %w", err)
	}
	pruneNoise(&root)

	var buf bytes.Buffer
	enc := v3yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	err := enc.Encode(&root)
	if cerr := enc.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("re-encoding rendered yaml: %w", err)
	}
	return buf.Bytes(), nil
}

// pruneNoise drops noise entries from n in place.
func pruneNoise(n *v3yaml.Node) {
	switch n.Kind {
	case v3yaml.DocumentNode:
		for _, c := range n.Content {
			pruneNoise(c)
		}
	case v3yaml.MappingNode:
		kept := n.Content[:0]
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			pruneNoise(val)
			if noiseKeys[key.Value] && isEmptyValue(val) {
				continue
			}
			kept = append(kept, key, val)
		}
		n.Content = kept
	case v3yaml.SequenceNode:
		kept := n.Content[:0]
		for _, item := range n.Content {
			// An item emptied by pruning is itself noise; a literal empty
			// item the author wrote (e.g. a `- {}` placeholder) is content
			// and stays.
			wasEmpty := item.Kind == v3yaml.MappingNode && len(item.Content) == 0
			pruneNoise(item)
			if !wasEmpty && item.Kind == v3yaml.MappingNode && len(item.Content) == 0 {
				continue
			}
			kept = append(kept, item)
		}
		n.Content = kept
	}
}

// isEmptyValue reports whether a node renders as null or an empty mapping.
func isEmptyValue(v *v3yaml.Node) bool {
	return v.Tag == "!!null" || (v.Kind == v3yaml.MappingNode && len(v.Content) == 0)
}
