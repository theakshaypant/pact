package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
)

// fakeClient records the runner's cluster operations and scripts results.
// Its GetPipelineRun always reports a terminal condition so the stream
// follower (which is real code) returns without polling delays.
type fakeClient struct {
	ensuredNamespaces []string
	pvcNS, pvcName    string
	pvcSize           string
	uploadedNS        string
	archive           []byte
	submitted         []*tektonv1.PipelineRun
	followResults     map[string]waitResult
	deletedNamespaces []string

	ensureNSErr error
	pvcErr      error
	uploadErr   error
	getErr      error // persistent GetPipelineRun error
	deleteErr   error
}

type waitResult struct {
	success bool
	message string
}

func (f *fakeClient) EnsureNamespace(_ context.Context, name string) error {
	f.ensuredNamespaces = append(f.ensuredNamespaces, name)
	return f.ensureNSErr
}

func (f *fakeClient) CreatePVC(_ context.Context, ns, name, size string) error {
	f.pvcNS, f.pvcName, f.pvcSize = ns, name, size
	return f.pvcErr
}

func (f *fakeClient) UploadSource(_ context.Context, ns, _ string, archive io.Reader) error {
	f.uploadedNS = ns
	var err error
	f.archive, err = io.ReadAll(archive)
	if err != nil {
		return err
	}
	return f.uploadErr
}

// Submit records the PipelineRun and derives a name like the server
// would: pr.Name if set, else generateName plus a suffix.
func (f *fakeClient) Submit(_ context.Context, pr *tektonv1.PipelineRun) (string, error) {
	f.submitted = append(f.submitted, pr)
	if pr.Name != "" {
		return pr.Name, nil
	}
	return pr.GenerateName + "x7q4", nil
}

// GetPipelineRun returns a terminal PipelineRun from the scripted
// results.
func (f *fakeClient) GetPipelineRun(_ context.Context, _, name string) (*tektonv1.PipelineRun, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	r, ok := f.followResults[name]
	if !ok {
		return nil, errors.New("no scripted result for " + name)
	}
	status := corev1.ConditionFalse
	if r.success {
		status = corev1.ConditionTrue
	}
	return outcomeCondition(status, r.message), nil
}

func (f *fakeClient) ListTaskRuns(context.Context, string, string) ([]tektonv1.TaskRun, error) {
	return nil, nil
}

func (f *fakeClient) PodLogs(context.Context, string, string, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeClient) DeleteNamespace(_ context.Context, name string) error {
	f.deletedNamespaces = append(f.deletedNamespaces, name)
	return f.deleteErr
}

// runPipeline builds a resolved PipelineRun with a source workspace and
// a clone task.
func runPipeline(name string) *tektonv1.PipelineRun {
	pr := wsPipeline(name, "source")
	pr.Spec.PipelineSpec.Tasks = []tektonv1.PipelineTask{
		{Name: "git-clone", TaskRef: &tektonv1.TaskRef{Name: "git-clone"}},
		{Name: "build", TaskSpec: &tektonv1.EmbeddedTask{}, RunAfter: []string{"git-clone"}},
	}
	return pr
}

// newTestRunner wires a Runner with a scripted fake client.
func newTestRunner(t *testing.T, client *fakeClient) (*Runner, *bytes.Buffer) {
	t.Helper()
	var progress bytes.Buffer
	return New(func([]byte) (Client, error) { return client, nil }, &progress), &progress
}

func TestRunnerRunEndToEndWithFakes(t *testing.T) {
	client := &fakeClient{followResults: map[string]waitResult{
		"pr-a": {success: true},
		"pr-b": {success: false, message: "task build failed"},
	}}
	r, progress := newTestRunner(t, client)
	repo := newSourceRepo(t)

	result, err := r.Run(context.Background(), []byte("kc"),
		[]*tektonv1.PipelineRun{runPipeline("pr-a"), runPipeline("pr-b")},
		Options{RepoRoot: repo})
	require.NoError(t, err)

	// Both runs share one isolated namespace, PVC and uploaded source.
	require.Len(t, client.ensuredNamespaces, 1)
	ns := client.ensuredNamespaces[0]
	assert.True(t, strings.HasPrefix(ns, "pact-run-"), "namespace %q must be a pact-run namespace", ns)
	assert.Equal(t, ns, client.pvcNS)
	assert.Equal(t, "pact-source", client.pvcName)
	assert.Equal(t, "1Gi", client.pvcSize, "default workspace size")
	assert.Equal(t, ns, client.uploadedNS)

	// The uploaded archive is the packaged repo: uncommitted content in.
	names, contents := readArchive(t, bytes.NewBuffer(client.archive))
	assert.Equal(t, "UNCOMMITTED", contents["README.md"])
	assert.NotContains(t, names, "ignored.txt")

	// Both PipelineRuns were mutated and submitted in the namespace.
	require.Len(t, client.submitted, 2)
	for _, pr := range client.submitted {
		assert.Equal(t, ns, pr.Namespace)
		require.Len(t, pr.Spec.Workspaces, 1)
		assert.Equal(t, "source", pr.Spec.Workspaces[0].Name)
		assert.Equal(t, "pact-source", pr.Spec.Workspaces[0].PersistentVolumeClaim.ClaimName)
		var taskNames []string
		for _, task := range pr.Spec.PipelineSpec.Tasks {
			taskNames = append(taskNames, task.Name)
		}
		assert.Equal(t, []string{"build"}, taskNames, "clone task must be dropped")
		assert.Empty(t, pr.Spec.PipelineSpec.Tasks[0].RunAfter, "runAfter must be pruned")
	}

	// Results and progress tell the story.
	require.Len(t, result.Runs, 2)
	assert.True(t, result.Runs[0].Success)
	assert.False(t, result.Runs[1].Success)
	assert.Equal(t, "task build failed", result.Runs[1].Message)
	assert.Equal(t, 1, result.FailedCount())
	out := progress.String()
	assert.Contains(t, out, "packaging source")
	assert.Contains(t, out, "uploading source (4 files")
	assert.Contains(t, out, `dropped clone task "git-clone"`)
	assert.Contains(t, out, `bound workspace "source"`)
	assert.Contains(t, out, "streaming logs")
	assert.Contains(t, out, "succeeded")
	assert.Contains(t, out, "failed")

	// A failed run keeps the namespace for inspection.
	assert.Equal(t, ns, result.Namespace)
	assert.False(t, result.Cleaned)
	assert.Empty(t, client.deletedNamespaces)
	assert.Contains(t, out, "kept for inspection")
}

func TestRunnerRunOptionsPlumbThrough(t *testing.T) {
	client := &fakeClient{followResults: map[string]waitResult{"pr-a": {success: true}}}
	r, _ := newTestRunner(t, client)

	_, err := r.Run(context.Background(), []byte("kc"), []*tektonv1.PipelineRun{runPipeline("pr-a")},
		Options{RepoRoot: newSourceRepo(t), Workspace: "source", WorkspaceSize: "5Gi", KeepCloneTasks: true})
	require.NoError(t, err)
	assert.Equal(t, "5Gi", client.pvcSize)
	require.Len(t, client.submitted, 1)
	assert.Len(t, client.submitted[0].Spec.PipelineSpec.Tasks, 2, "--keep-clone-tasks must preserve clone tasks")
}

func TestRunnerRunErrorPaths(t *testing.T) {
	repo := newSourceRepo(t)
	tests := []struct {
		name    string
		client  *fakeClient
		timeout time.Duration // zero: plain background context
		want    string
	}{
		{name: "namespace fails", client: &fakeClient{ensureNSErr: errors.New("quota exceeded")}, want: "preparing namespace"},
		{name: "pvc fails", client: &fakeClient{pvcErr: errors.New("no storage class")}, want: "preparing source volume"},
		{name: "upload fails", client: &fakeClient{uploadErr: errors.New("pod crashed")}, want: "uploading source"},
		{name: "follow fails", client: &fakeClient{getErr: errors.New("watch died")}, timeout: 250 * time.Millisecond, want: "following pipelinerun"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.client.followResults = map[string]waitResult{"pr-a": {success: true}}
			ctx := context.Background()
			if tt.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.timeout)
				defer cancel()
			}
			r, _ := newTestRunner(t, tt.client)
			_, err := r.Run(ctx, []byte("kc"),
				[]*tektonv1.PipelineRun{runPipeline("pr-a")}, Options{RepoRoot: repo})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestRunnerRunRejectsEmpty(t *testing.T) {
	r, _ := newTestRunner(t, &fakeClient{})
	_, err := r.Run(context.Background(), []byte("kc"), nil, Options{RepoRoot: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pipelineruns")
}

func TestRunnerRunHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeClient{}
	r, _ := newTestRunner(t, client)
	_, err := r.Run(ctx, []byte("kc"), []*tektonv1.PipelineRun{runPipeline("pr-a")},
		Options{RepoRoot: newSourceRepo(t)})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "want context.Canceled, got %v", err)
}

// TestRunnerRunGenerateNameDerivedServerSide pins the flow PAC resolve
// actually produces: generateName-only PipelineRuns whose name the API
// server derives on create.
func TestRunnerRunGenerateNameDerivedServerSide(t *testing.T) {
	client := &fakeClient{followResults: map[string]waitResult{"phase3-verify-x7q4": {success: true}}}
	r, progress := newTestRunner(t, client)

	pr := runPipeline("")
	pr.Name, pr.GenerateName = "", "phase3-verify-"

	result, err := r.Run(context.Background(), []byte("kc"), []*tektonv1.PipelineRun{pr},
		Options{RepoRoot: newSourceRepo(t)})
	require.NoError(t, err)
	require.Len(t, result.Runs, 1)
	assert.Equal(t, "phase3-verify-x7q4", result.Runs[0].Name, "the server-derived name must flow through")
	assert.True(t, result.Runs[0].Success)

	out := progress.String()
	assert.Contains(t, out, "run 1/1: phase3-verify", "the display name must fall back to generateName")
	assert.Contains(t, out, "submitted as phase3-verify-x7q4")
}

// TestRunnerRunCleanupPolicy pins when the run namespace is deleted.
func TestRunnerRunCleanupPolicy(t *testing.T) {
	newClient := func(success bool) *fakeClient {
		return &fakeClient{followResults: map[string]waitResult{"pr-a": {success: success}}}
	}

	t.Run("deletes the namespace when everything succeeded", func(t *testing.T) {
		client := newClient(true)
		r, progress := newTestRunner(t, client)
		result, err := r.Run(context.Background(), []byte("kc"),
			[]*tektonv1.PipelineRun{runPipeline("pr-a")}, Options{RepoRoot: newSourceRepo(t)})
		require.NoError(t, err)
		assert.True(t, result.Cleaned)
		require.Len(t, client.deletedNamespaces, 1)
		assert.Equal(t, result.Namespace, client.deletedNamespaces[0])
		assert.Contains(t, progress.String(), "cleaned up namespace")
	})

	t.Run("keeps the namespace when a run failed", func(t *testing.T) {
		client := newClient(false)
		r, _ := newTestRunner(t, client)
		result, err := r.Run(context.Background(), []byte("kc"),
			[]*tektonv1.PipelineRun{runPipeline("pr-a")}, Options{RepoRoot: newSourceRepo(t)})
		require.NoError(t, err)
		assert.False(t, result.Cleaned)
		assert.Empty(t, client.deletedNamespaces)
	})

	t.Run("keeps the namespace with NoCleanup even on success", func(t *testing.T) {
		client := newClient(true)
		r, _ := newTestRunner(t, client)
		result, err := r.Run(context.Background(), []byte("kc"),
			[]*tektonv1.PipelineRun{runPipeline("pr-a")},
			Options{RepoRoot: newSourceRepo(t), NoCleanup: true})
		require.NoError(t, err)
		assert.False(t, result.Cleaned)
		assert.Empty(t, client.deletedNamespaces)
	})

	t.Run("deletes the namespace when the setup failed before submitting", func(t *testing.T) {
		client := &fakeClient{uploadErr: errors.New("pod crashed")}
		r, _ := newTestRunner(t, client)
		result, err := r.Run(context.Background(), []byte("kc"),
			[]*tektonv1.PipelineRun{runPipeline("pr-a")}, Options{RepoRoot: newSourceRepo(t)})
		require.Error(t, err)
		assert.True(t, result.Cleaned, "nothing was submitted, nothing to inspect")
		require.Len(t, client.deletedNamespaces, 1)
	})

	t.Run("keeps the namespace when following failed after submitting", func(t *testing.T) {
		client := &fakeClient{getErr: errors.New("watch died"),
			followResults: map[string]waitResult{"pr-a": {success: true}}}
		r, _ := newTestRunner(t, client)
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		result, err := r.Run(ctx, []byte("kc"),
			[]*tektonv1.PipelineRun{runPipeline("pr-a")}, Options{RepoRoot: newSourceRepo(t)})
		require.Error(t, err)
		assert.False(t, result.Cleaned)
		assert.Empty(t, client.deletedNamespaces)
	})

	t.Run("a delete failure is a warning, not an error", func(t *testing.T) {
		client := newClient(true)
		client.deleteErr = errors.New("namespace stuck")
		r, progress := newTestRunner(t, client)
		result, err := r.Run(context.Background(), []byte("kc"),
			[]*tektonv1.PipelineRun{runPipeline("pr-a")}, Options{RepoRoot: newSourceRepo(t)})
		require.NoError(t, err)
		assert.False(t, result.Cleaned)
		assert.Contains(t, progress.String(), "warning: cleaning up namespace")
	})
}

func TestRunNamespaceIsRandom(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 16; i++ {
		ns, err := runNamespace()
		require.NoError(t, err)
		require.Len(t, ns, len("pact-run-")+8)
		require.False(t, seen[ns], "namespaces must not repeat")
		seen[ns] = true
	}
}

func TestByteCount(t *testing.T) {
	assert.Equal(t, "512B", byteCount(512))
	assert.Equal(t, "1.5KB", byteCount(1536))
	assert.Equal(t, "2.0MB", byteCount(2<<20))
}
