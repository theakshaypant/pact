package pac

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"github.com/theakshaypant/pact/pkg/event"
	"sigs.k8s.io/yaml"
)

var yamlDocRe = regexp.MustCompile(`(?m)^---\s*$`)

// parsePipelineRuns splits a multi-doc YAML stream and decodes each
// PipelineRun document.
func parsePipelineRuns(t *testing.T, docs string) []*tektonv1.PipelineRun {
	t.Helper()
	var prs []*tektonv1.PipelineRun
	for _, doc := range yamlDocRe.Split(docs, -1) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var pr tektonv1.PipelineRun
		require.NoError(t, yaml.Unmarshal([]byte(doc), &pr), "document: %s", doc)
		prs = append(prs, &pr)
	}
	return prs
}

func TestResolveBasicInlinesLocalTask(t *testing.T) {
	eng := New(nil)
	out, err := eng.Resolve(context.Background(), fixtureDir+"/basic", matchEvent(t, event.PullRequest, []string{"src/main.go"}))
	require.NoError(t, err)

	prs := parsePipelineRuns(t, string(out))
	// pull_request on main matches: pr-only, local-task, two-runs-pr
	require.Len(t, prs, 3)

	byName := map[string]*tektonv1.PipelineRun{}
	for _, pr := range prs {
		byName[pr.GenerateName] = pr
	}

	// The local task defined in task-def.yaml must be inlined as a taskSpec.
	localTask, ok := byName["local-task-"]
	require.True(t, ok, "want a local-task pipelinerun, got %v", byName)
	require.NotNil(t, localTask.Spec.PipelineSpec)
	require.Len(t, localTask.Spec.PipelineSpec.Tasks, 1)
	task := localTask.Spec.PipelineSpec.Tasks[0]
	assert.Nil(t, task.TaskRef, "taskRef must be replaced by an inline taskSpec")
	require.NotNil(t, task.TaskSpec)
	require.NotNil(t, task.TaskSpec.Steps)
	assert.Equal(t, "echo local task", task.TaskSpec.Steps[0].Script)

	// Output hygiene: no noise blocks, TypeMeta set, generateName used.
	assert.NotContains(t, string(out), "creationTimestamp: null")
	for _, pr := range prs {
		assert.Equal(t, tektonv1.SchemeGroupVersion.String(), pr.APIVersion)
		assert.Equal(t, "PipelineRun", pr.Kind)
		assert.NotEmpty(t, pr.GenerateName)
	}
}

func TestResolveTemplatedParams(t *testing.T) {
	eng := New(nil)
	out, err := eng.Resolve(context.Background(), fixtureDir+"/resolve", matchEvent(t, event.PullRequest, nil))
	require.NoError(t, err)

	prs := parsePipelineRuns(t, string(out))
	require.Len(t, prs, 1)
	// The templated fixture's only PipelineRun is "templated".
	require.NotNil(t, prs[0].Spec.Params)

	var revision, repo string
	for _, p := range prs[0].Spec.Params {
		switch p.Name {
		case "revision":
			revision = p.Value.StringVal
		case "repo":
			repo = p.Value.StringVal
		}
	}
	assert.Equal(t, "abcdef", revision, "{{ revision }} must be substituted with the HEAD SHA")
	assert.Equal(t, "git@github.com:example/repo.git", repo, "{{ repo_url }} must be substituted with the clone URL")
}

func TestResolveCommentSubstitutesTriggerComment(t *testing.T) {
	eng := New(nil)
	ev := matchEventOpts(t, event.Options{Comment: "/test please"}, nil)
	out, err := eng.Resolve(context.Background(), fixtureDir+"/labelcomment", ev)
	require.NoError(t, err)

	prs := parsePipelineRuns(t, string(out))
	// Only the on-comment PipelineRun survives a comment event.
	require.Len(t, prs, 1)
	require.NotNil(t, prs[0].Spec.Params)
	require.Len(t, prs[0].Spec.Params, 1)
	assert.Equal(t, "comment", prs[0].Spec.Params[0].Name)
	assert.Equal(t, "/test please", prs[0].Spec.Params[0].Value.StringVal,
		"{{ trigger_comment }} must be substituted with the comment body")
}

func TestResolveNoMatchDescribesEvent(t *testing.T) {
	tests := []struct {
		name string
		opts event.Options
		want []string
	}{
		{"labeled", event.Options{Labels: []string{"kind/lgtm"}},
			[]string{"labeled pull_request event", "labels: kind/lgtm"}},
		{"comment", event.Options{Comment: "/test please"},
			[]string{"pull_request comment event", `comment: "/test please"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := New(nil)
			ev := matchEventOpts(t, tt.opts, nil)
			_, err := eng.Resolve(context.Background(), fixtureDir+"/nomatch", ev)
			require.Error(t, err)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestResolveRemoteRefFailsOffline(t *testing.T) {
	ev := matchEvent(t, event.PullRequest, nil)
	eng := New(nil)
	_, err := eng.Resolve(context.Background(), fixtureDir+"/remote", ev)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot find referenced task")
	assert.Contains(t, err.Error(), "offline mode")
}

func TestResolveNoMatchErrors(t *testing.T) {
	ev := matchEvent(t, event.Push, nil)
	eng := New(nil)
	_, err := eng.Resolve(context.Background(), fixtureDir+"/nomatch", ev)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pipelinerun")
}

func TestResolveCustomParams(t *testing.T) {
	// environment is wired through a {{ }} placeholder, greeting only
	// through a declared pipelineSpec param: both must land in spec.params.
	eng := New(nil, WithParams(Params{"environment": "prod", "greeting": "hi"}))
	out, err := eng.Resolve(context.Background(), fixtureDir+"/params", matchEvent(t, event.PullRequest, nil))
	require.NoError(t, err)

	prs := parsePipelineRuns(t, string(out))
	require.Len(t, prs, 1)

	params := map[string]string{}
	for _, p := range prs[0].Spec.Params {
		params[p.Name] = p.Value.StringVal
	}
	assert.Equal(t, "prod", params["environment"], "{{ environment }} must be substituted")
	assert.Equal(t, "hi", params["greeting"], "a declared param with no spec.params entry must be appended")
	assert.NotContains(t, string(out), "{{", "no unsubstituted placeholders may remain")
}

func TestResolveCustomParamsUnknownIsAnError(t *testing.T) {
	eng := New(nil, WithParams(Params{"environment": "prod", "typo": "x"}))
	_, err := eng.Resolve(context.Background(), fixtureDir+"/params", matchEvent(t, event.PullRequest, nil))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownParams)
	assert.Contains(t, err.Error(), `"typo"`)
	assert.NotContains(t, err.Error(), `"environment"`, "used names must not be reported")
}

func TestResolveWithoutCustomParamsLeavesManifestAlone(t *testing.T) {
	// Without params the {{ environment }} placeholder stays literal, as
	// PAC leaves unknown keys: nothing errors, nothing is appended.
	eng := New(nil)
	out, err := eng.Resolve(context.Background(), fixtureDir+"/params", matchEvent(t, event.PullRequest, nil))
	require.NoError(t, err)

	prs := parsePipelineRuns(t, string(out))
	require.Len(t, prs, 1)
	require.Len(t, prs[0].Spec.Params, 1)
	assert.Equal(t, "environment", prs[0].Spec.Params[0].Name)
	assert.Equal(t, "{{ environment }}", prs[0].Spec.Params[0].Value.StringVal)
}
