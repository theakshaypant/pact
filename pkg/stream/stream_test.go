package stream

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	duckv1 "knative.dev/pkg/apis/duck/v1"
)

// fakeSource scripts a Follow run: a sequence of PipelineRun states and
// TaskRun snapshots (each advancing per tick, the last repeating), a
// per-container log script, and counters for assertions.
type fakeSource struct {
	mu      sync.Mutex
	prs     []*tektonv1.PipelineRun // returned in order, last repeats
	prIdx   int
	getErrs int // first N GetPipelineRun calls fail

	ticks   [][]tektonv1.TaskRun // returned in order, last repeats
	tickIdx int

	logs  map[string]*logScript // key pod+"/"+container
	opens map[string]int

	// podLogsOverride, when set, serves opens instead of the log
	// scripts (used for live io.Pipe streams).
	podLogsOverride func(key string) (io.ReadCloser, error)

	requestedRun string // last pipelineRun name passed to ListTaskRuns
}

type logScript struct {
	failFor int // first N opens error before the body is served
	body    string
	err     error // permanent open error
}

func newFakeSource(prs []*tektonv1.PipelineRun, ticks [][]tektonv1.TaskRun, logs map[string]*logScript) *fakeSource {
	return &fakeSource{prs: prs, ticks: ticks, logs: logs, opens: map[string]int{}}
}

func (f *fakeSource) GetPipelineRun(_ context.Context, _, _ string) (*tektonv1.PipelineRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErrs > 0 {
		f.getErrs--
		return nil, errors.New("transient api error")
	}
	pr := f.prs[min(f.prIdx, len(f.prs)-1)]
	f.prIdx++
	return pr, nil
}

func (f *fakeSource) ListTaskRuns(_ context.Context, _, pipelineRun string) ([]tektonv1.TaskRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requestedRun = pipelineRun
	tick := f.ticks[min(f.tickIdx, len(f.ticks)-1)]
	f.tickIdx++
	return tick, nil
}

func (f *fakeSource) PodLogs(_ context.Context, _, pod, container string) (io.ReadCloser, error) {
	key := pod + "/" + container
	f.mu.Lock()
	f.opens[key]++
	script := f.logs[key]
	opens := f.opens[key]
	override := f.podLogsOverride
	f.mu.Unlock()
	if override != nil {
		return override(key)
	}
	if script == nil {
		return nil, errors.New("no script for " + key)
	}
	if script.err != nil {
		return nil, script.err
	}
	if opens <= script.failFor {
		return nil, errors.New("container is waiting to start")
	}
	return io.NopCloser(strings.NewReader(script.body)), nil
}

func (f *fakeSource) openCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens[key]
}

// speedUp shrinks the poll and retry tunables, restoring them on cleanup.
func speedUp(t *testing.T) {
	t.Helper()
	origPoll, origDrain, origDelay := pollInterval, drainTimeout, logRetryDelay
	pollInterval, drainTimeout, logRetryDelay = time.Millisecond, time.Second, time.Millisecond
	t.Cleanup(func() {
		pollInterval, drainTimeout, logRetryDelay = origPoll, origDrain, origDelay
	})
}

// prWithCondition builds a PipelineRun with the given Succeeded condition.
func prWithCondition(status corev1.ConditionStatus, message string) *tektonv1.PipelineRun {
	return &tektonv1.PipelineRun{
		Status: tektonv1.PipelineRunStatus{
			Status: duckv1.Status{Conditions: duckv1.Conditions{{
				Type: "Succeeded", Status: status, Message: message,
			}}},
		},
	}
}

func runningPR() *tektonv1.PipelineRun { return prWithCondition(corev1.ConditionUnknown, "") }
func succeededPR() *tektonv1.PipelineRun {
	return prWithCondition(corev1.ConditionTrue, "Tasks Completed: 1")
}
func failedPR() *tektonv1.PipelineRun { return prWithCondition(corev1.ConditionFalse, "step failed") }

// taskRunWithSteps builds a TaskRun with a pipelineTask label, a pod and
// step states.
func taskRunWithSteps(name, task, pod string, steps ...tektonv1.StepState) tektonv1.TaskRun {
	return tektonv1.TaskRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{pipeline.PipelineTaskLabelKey: task},
		},
		Status: tektonv1.TaskRunStatus{
			TaskRunStatusFields: tektonv1.TaskRunStatusFields{PodName: pod, Steps: steps},
		},
	}
}

// startedStep is a step whose container has begun running.
func startedStep(name string) tektonv1.StepState {
	return tektonv1.StepState{
		Name:      name,
		Container: "step-" + name,
		ContainerState: corev1.ContainerState{
			Running: &corev1.ContainerStateRunning{},
		},
	}
}

// waitingStep is a step that has not started yet.
func waitingStep(name string) tektonv1.StepState {
	return tektonv1.StepState{
		Name:      name,
		Container: "step-" + name,
		ContainerState: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{},
		},
	}
}

func TestFollowStreamsStepWithTaskPrefix(t *testing.T) {
	speedUp(t)
	tr := taskRunWithSteps("tr-1", "build", "pod-1", startedStep("compile"))
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), succeededPR()},
		[][]tektonv1.TaskRun{{tr}},
		map[string]*logScript{"pod-1/step-compile": {body: "compiling\n"}},
	)

	var out strings.Builder
	res, err := Follow(context.Background(), src, "ns", "pr-1", &out)
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Equal(t, "Tasks Completed: 1", res.Message)
	assert.Equal(t, "[build : compile] compiling\n", out.String())
	assert.Equal(t, "pr-1", src.requestedRun)
}

func TestFollowSkipsWaitingSteps(t *testing.T) {
	speedUp(t)
	tr := taskRunWithSteps("tr-1", "build", "pod-1", startedStep("compile"), waitingStep("test"))
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), succeededPR()},
		[][]tektonv1.TaskRun{{tr}},
		map[string]*logScript{
			"pod-1/step-compile": {body: "compiling\n"},
			"pod-1/step-test":    {body: "should not appear\n"},
		},
	)

	var out strings.Builder
	_, err := Follow(context.Background(), src, "ns", "pr-1", &out)
	require.NoError(t, err)
	assert.NotContains(t, out.String(), "should not appear")
	assert.Zero(t, src.openCount("pod-1/step-test"), "waiting step must not be opened")
}

func TestFollowStreamsStepOnceItStarts(t *testing.T) {
	speedUp(t)
	waiting := taskRunWithSteps("tr-1", "build", "pod-1", startedStep("compile"), waitingStep("test"))
	started := taskRunWithSteps("tr-1", "build", "pod-1", startedStep("compile"), startedStep("test"))
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), runningPR(), succeededPR()},
		[][]tektonv1.TaskRun{{waiting}, {started}},
		map[string]*logScript{
			"pod-1/step-compile": {body: "compiling\n"},
			"pod-1/step-test":    {body: "testing\n"},
		},
	)

	var out strings.Builder
	_, err := Follow(context.Background(), src, "ns", "pr-1", &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "[build : test] testing\n")
	assert.Equal(t, 1, src.openCount("pod-1/step-compile"), "dedup: compile opened exactly once across ticks")
	assert.Equal(t, 1, src.openCount("pod-1/step-test"))
}

func TestFollowDrainsAlreadyTerminatedLogs(t *testing.T) {
	speedUp(t)
	tr := taskRunWithSteps("tr-1", "build", "pod-1", startedStep("compile"))
	src := newFakeSource(
		[]*tektonv1.PipelineRun{succeededPR()},
		[][]tektonv1.TaskRun{{tr}},
		map[string]*logScript{"pod-1/step-compile": {body: "late log\n"}},
	)

	var out strings.Builder
	res, err := Follow(context.Background(), src, "ns", "pr-1", &out)
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Contains(t, out.String(), "[build : compile] late log\n")
}

func TestFollowRetriesTransientGetErrors(t *testing.T) {
	speedUp(t)
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), succeededPR()},
		[][]tektonv1.TaskRun{{}},
		map[string]*logScript{},
	)
	src.getErrs = 1

	res, err := Follow(context.Background(), src, "ns", "pr-1", io.Discard)
	require.NoError(t, err)
	assert.True(t, res.Success)
}

func TestFollowRetriesLogOpen(t *testing.T) {
	speedUp(t)
	tr := taskRunWithSteps("tr-1", "build", "pod-1", startedStep("compile"))
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), succeededPR()},
		[][]tektonv1.TaskRun{{tr}},
		map[string]*logScript{"pod-1/step-compile": {failFor: 2, body: "finally\n"}},
	)

	var out strings.Builder
	_, err := Follow(context.Background(), src, "ns", "pr-1", &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "[build : compile] finally\n")
	assert.Equal(t, 3, src.openCount("pod-1/step-compile"))
}

func TestFollowNotifiesWhenLogsUnavailable(t *testing.T) {
	speedUp(t)
	tr := taskRunWithSteps("tr-1", "build", "pod-1", startedStep("compile"))
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), succeededPR()},
		[][]tektonv1.TaskRun{{tr}},
		map[string]*logScript{"pod-1/step-compile": {err: errors.New("pod is gone")}},
	)

	var out strings.Builder
	_, err := Follow(context.Background(), src, "ns", "pr-1", &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "[build : compile] (logs unavailable: pod is gone)\n")
}

func TestFollowStreamsRetryPod(t *testing.T) {
	speedUp(t)
	first := taskRunWithSteps("tr-1", "flaky", "pod-1", startedStep("run"))
	retry := taskRunWithSteps("tr-1", "flaky", "pod-2", startedStep("run"))
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), runningPR(), succeededPR()},
		[][]tektonv1.TaskRun{{first}, {retry}},
		map[string]*logScript{
			"pod-1/step-run": {body: "attempt one\n"},
			"pod-2/step-run": {body: "attempt two\n"},
		},
	)

	var out strings.Builder
	_, err := Follow(context.Background(), src, "ns", "pr-1", &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "[flaky : run] attempt one\n")
	assert.Contains(t, out.String(), "[flaky : run] attempt two\n")
}

func TestFollowPropagatesFailure(t *testing.T) {
	speedUp(t)
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), failedPR()},
		[][]tektonv1.TaskRun{{}},
		map[string]*logScript{},
	)

	res, err := Follow(context.Background(), src, "ns", "pr-1", io.Discard)
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "step failed", res.Message)
}

func TestFollowHonoursContextCancellation(t *testing.T) {
	speedUp(t)
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR()},
		[][]tektonv1.TaskRun{{}},
		map[string]*logScript{},
	)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := Follow(ctx, src, "ns", "pr-1", io.Discard)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "want context.Canceled, got %v", err)
}

// TestFollowDoesNotTearParallelLines blasts two pipes concurrently and
// asserts every emitted line is complete and correctly prefixed.
func TestFollowDoesNotTearParallelLines(t *testing.T) {
	speedUp(t)
	const lines = 200

	buildPipe, buildWriter := io.Pipe()
	testPipe, testWriter := io.Pipe()
	taskRuns := []tektonv1.TaskRun{
		taskRunWithSteps("tr-1", "build", "pod-1", startedStep("compile")),
		taskRunWithSteps("tr-2", "test", "pod-2", startedStep("verify")),
	}
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), runningPR(), succeededPR()},
		[][]tektonv1.TaskRun{{taskRuns[0], taskRuns[1]}},
		map[string]*logScript{},
	)
	src.podLogsOverride = func(key string) (io.ReadCloser, error) {
		switch key {
		case "pod-1/step-compile":
			return buildPipe, nil
		case "pod-2/step-verify":
			return testPipe, nil
		}
		return nil, errors.New("unexpected key " + key)
	}

	for _, w := range []*io.PipeWriter{buildWriter, testWriter} {
		go func(w *io.PipeWriter) {
			for range lines {
				_, _ = w.Write([]byte(strings.Repeat("x", 64) + "\n"))
			}
			_ = w.Close()
		}(w)
	}

	var out strings.Builder
	_, err := Follow(context.Background(), src, "ns", "pr-1", &out)
	require.NoError(t, err)

	emitted := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, emitted, 2*lines)
	for _, line := range emitted {
		assert.True(t, strings.HasPrefix(line, "[build : compile] ") || strings.HasPrefix(line, "[test : verify] "),
			"torn or wrongly prefixed line: %q", line)
	}
}

func TestFollowEmitsPartialLastLine(t *testing.T) {
	speedUp(t)
	tr := taskRunWithSteps("tr-1", "build", "pod-1", startedStep("compile"))
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), succeededPR()},
		[][]tektonv1.TaskRun{{tr}},
		map[string]*logScript{"pod-1/step-compile": {body: "full line\npartial tail"}},
	)

	var out strings.Builder
	_, err := Follow(context.Background(), src, "ns", "pr-1", &out)
	require.NoError(t, err)
	assert.Equal(t, "[build : compile] full line\n[build : compile] partial tail\n", out.String())
}

func TestFollowPassesThroughLongLines(t *testing.T) {
	speedUp(t)
	long := strings.Repeat("y", 100*1024)
	tr := taskRunWithSteps("tr-1", "build", "pod-1", startedStep("compile"))
	src := newFakeSource(
		[]*tektonv1.PipelineRun{runningPR(), succeededPR()},
		[][]tektonv1.TaskRun{{tr}},
		map[string]*logScript{"pod-1/step-compile": {body: long + "\n"}},
	)

	var out strings.Builder
	_, err := Follow(context.Background(), src, "ns", "pr-1", &out)
	require.NoError(t, err)
	assert.Equal(t, "[build : compile] "+long+"\n", out.String())
}

func TestOutcome(t *testing.T) {
	tests := []struct {
		name        string
		pr          *tektonv1.PipelineRun
		wantDone    bool
		wantSuccess bool
		wantMessage string
	}{{
		name:        "succeeded",
		pr:          prWithCondition(corev1.ConditionTrue, "all good"),
		wantDone:    true,
		wantSuccess: true,
		wantMessage: "all good",
	}, {
		name:        "failed",
		pr:          prWithCondition(corev1.ConditionFalse, "nope"),
		wantDone:    true,
		wantMessage: "nope",
	}, {
		name: "running",
		pr:   prWithCondition(corev1.ConditionUnknown, ""),
	}, {
		name: "no condition",
		pr:   &tektonv1.PipelineRun{},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done, success, message := outcome(tt.pr)
			assert.Equal(t, tt.wantDone, done)
			assert.Equal(t, tt.wantSuccess, success)
			assert.Equal(t, tt.wantMessage, message)
		})
	}
}

func TestCopyLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{{
		name: "plain lines",
		in:   "one\ntwo\n",
		want: "[t : s] one\n[t : s] two\n",
	}, {
		name: "partial tail",
		in:   "one\ntail",
		want: "[t : s] one\n[t : s] tail\n",
	}, {
		name: "empty body",
		in:   "",
		want: "",
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			require.NoError(t, copyLines(strings.NewReader(tt.in), "[t : s] ", &out))
			assert.Equal(t, tt.want, out.String())
		})
	}
}
