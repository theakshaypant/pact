// Package tekton validates pact's resolved output with Tekton's own
// admission-level validation, proving the YAML pact prints is a genuinely
// submittable PipelineRun rather than merely parseable YAML.
package tekton

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/yaml"
)

var docRe = regexp.MustCompile(`(?m)^---\s*$`)

// DecodePipelineRuns splits a multi-document YAML stream and decodes every
// document into a PipelineRun.
func DecodePipelineRuns(t *testing.T, docs string) []*tektonv1.PipelineRun {
	t.Helper()
	var prs []*tektonv1.PipelineRun
	for _, doc := range docRe.Split(docs, -1) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var pr tektonv1.PipelineRun
		require.NoError(t, yaml.Unmarshal([]byte(doc), &pr), "document: %s", doc)
		require.Equal(t, "PipelineRun", pr.Kind, "document: %s", doc)
		t.Logf("decoded PipelineRun %q (generateName %q)", pr.Name, pr.GenerateName)
		prs = append(prs, &pr)
	}
	return prs
}

// Validate runs every PipelineRun through the validation a Tekton
// apiserver would apply on admission.
func Validate(t *testing.T, prs []*tektonv1.PipelineRun) {
	t.Helper()
	ctx := apis.WithinSpec(context.Background())
	for _, pr := range prs {
		// Admission on a real apiserver first derives a concrete name from
		// generateName and runs defaulting (e.g. filling in param types),
		// then validates. Mirror that chain so untyped params — which PAC
		// users write all the time — are not spuriously rejected here.
		if pr.Name == "" {
			pr.Name = pr.GenerateName + "e2e"
		}
		pr.SetDefaults(ctx)
		if errs := pr.Validate(ctx); errs != nil {
			t.Errorf("pipelinerun %q failed Tekton validation: %v", pr.GenerateName, errs)
		}
	}
}

// ByGenerateName returns the PipelineRun whose generateName matches.
func ByGenerateName(t *testing.T, prs []*tektonv1.PipelineRun, generateName string) *tektonv1.PipelineRun {
	t.Helper()
	for _, pr := range prs {
		if pr.GenerateName == generateName {
			return pr
		}
	}
	t.Fatalf("no pipelinerun with generateName %q among %v", generateName, prs)
	return nil
}
