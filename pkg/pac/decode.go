package pac

import (
	"bytes"
	"fmt"
	"io"

	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

// DecodePipelineRuns decodes the hydrated multi-document YAML pact
// resolve produces into PipelineRuns.
func DecodePipelineRuns(data []byte) ([]*tektonv1.PipelineRun, error) {
	var runs []*tektonv1.PipelineRun
	dec := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var pr tektonv1.PipelineRun
		if err := dec.Decode(&pr); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decoding pipelinerun document %d: %w", len(runs)+1, err)
		}
		if pr.Kind == "" {
			continue // separator or comment-only document
		}
		if pr.Kind != "PipelineRun" {
			return nil, fmt.Errorf("document %d is a %s, not a PipelineRun", len(runs)+1, pr.Kind)
		}
		if pr.Name == "" && pr.GenerateName == "" {
			return nil, fmt.Errorf("pipelinerun document %d has neither name nor generateName", len(runs)+1)
		}
		runs = append(runs, &pr)
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("no pipelineruns found in resolved output")
	}
	return runs, nil
}
