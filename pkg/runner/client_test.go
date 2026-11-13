package runner

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	tektonfake "github.com/tektoncd/pipeline/pkg/client/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	duckv1 "knative.dev/pkg/apis/duck/v1"
)

// outcomeCondition builds a PipelineRun carrying one Succeeded condition.
func outcomeCondition(status corev1.ConditionStatus, message string) *tektonv1.PipelineRun {
	return &tektonv1.PipelineRun{
		Status: tektonv1.PipelineRunStatus{
			Status: duckv1.Status{
				Conditions: duckv1.Conditions{{
					Type:    "Succeeded",
					Status:  status,
					Reason:  "SomeReason",
					Message: message,
				}},
			},
		},
	}
}

// newFakeKubeClient wires a kubeClient over fake clientsets.
func newFakeKubeClient(coreObjs, tektonObjs []runtime.Object) *kubeClient {
	return &kubeClient{
		core:   k8sfake.NewSimpleClientset(coreObjs...),
		tekton: tektonfake.NewSimpleClientset(tektonObjs...).TektonV1(),
	}
}

// speedUpPolls shrinks the poll intervals and timeouts so fake-client
// tests run fast, restoring them on cleanup.
func speedUpPolls(t *testing.T) {
	t.Helper()
	oldInterval, oldSATimeout := podPollInterval, saReadyTimeout
	podPollInterval, saReadyTimeout = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() {
		podPollInterval, saReadyTimeout = oldInterval, oldSATimeout
	})
}

// defaultSA builds the namespace's default ServiceAccount.
func defaultSA(ns string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns}}
}

func TestEnsureNamespace(t *testing.T) {
	speedUpPolls(t)
	tests := []struct {
		name    string
		objects []runtime.Object
		wantErr string
	}{
		{
			name:    "creates the namespace once the default serviceaccount exists",
			objects: []runtime.Object{defaultSA("pact-run-x")},
		},
		{
			name: "tolerates an existing namespace",
			objects: []runtime.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pact-run-x"}},
				defaultSA("pact-run-x"),
			},
		},
		{
			name:    "times out when the serviceaccount never appears",
			objects: nil,
			wantErr: "waiting for namespace",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newFakeKubeClient(tt.objects, nil)
			err := c.EnsureNamespace(context.Background(), "pact-run-x")
			if tt.wantErr == "" {
				require.NoError(t, err)
				_, err := c.core.CoreV1().Namespaces().Get(context.Background(), "pact-run-x", metav1.GetOptions{})
				require.NoError(t, err, "the namespace must exist afterwards")
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestCreatePVC(t *testing.T) {
	t.Run("creates the claim with the requested size", func(t *testing.T) {
		c := newFakeKubeClient(nil, nil)
		require.NoError(t, c.CreatePVC(context.Background(), "ns", "pact-source", "5Gi"))

		pvc, err := c.core.CoreV1().PersistentVolumeClaims("ns").Get(context.Background(), "pact-source", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, pvc.Spec.AccessModes)
		want := resource.MustParse("5Gi")
		assert.Equal(t, want, pvc.Spec.Resources.Requests[corev1.ResourceStorage])
	})

	t.Run("rejects an unparsable size", func(t *testing.T) {
		c := newFakeKubeClient(nil, nil)
		err := c.CreatePVC(context.Background(), "ns", "pact-source", "banana")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing workspace size")
	})
}

func TestSubmit(t *testing.T) {
	c := newFakeKubeClient(nil, nil)
	pr := &tektonv1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{Name: "pr-a", GenerateName: "pr-"},
		Spec:       tektonv1.PipelineRunSpec{PipelineSpec: &tektonv1.PipelineSpec{}},
	}

	name, err := c.Submit(context.Background(), pr)
	require.NoError(t, err)
	assert.Equal(t, "pr-a", name, "an explicit name must pass through")
}

// prWithCondition builds a named PipelineRun carrying one Succeeded
// condition, for Wait tests.
func prWithCondition(name string, status corev1.ConditionStatus, message string) *tektonv1.PipelineRun {
	pr := outcomeCondition(status, message)
	pr.Name = name
	pr.Namespace = "ns"
	return pr
}

func TestGetPipelineRun(t *testing.T) {
	t.Run("returns the pipelinerun", func(t *testing.T) {
		c := newFakeKubeClient(nil, []runtime.Object{prWithCondition("pr-a", corev1.ConditionTrue, "done")})
		pr, err := c.GetPipelineRun(context.Background(), "ns", "pr-a")
		require.NoError(t, err)
		assert.Equal(t, "pr-a", pr.Name)
	})

	t.Run("wraps a missing pipelinerun", func(t *testing.T) {
		c := newFakeKubeClient(nil, nil)
		_, err := c.GetPipelineRun(context.Background(), "ns", "nope")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "getting pipelinerun nope in ns")
	})
}

// taskRunWithLabel builds a TaskRun labelled as a child of prName.
func taskRunWithLabel(name, prName string) *tektonv1.TaskRun {
	return &tektonv1.TaskRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "ns",
			Labels: map[string]string{
				"tekton.dev/pipelineRun":  prName,
				"tekton.dev/pipelineTask": "build",
			},
		},
	}
}

func TestListTaskRuns(t *testing.T) {
	ours := taskRunWithLabel("tr-ours", "pr-a")
	theirs := taskRunWithLabel("tr-theirs", "pr-b")
	c := newFakeKubeClient(nil, []runtime.Object{ours, theirs})

	trs, err := c.ListTaskRuns(context.Background(), "ns", "pr-a")
	require.NoError(t, err)
	require.Len(t, trs, 1, "only this pipelinerun's taskruns must be listed")
	assert.Equal(t, "tr-ours", trs[0].Name)
}

func TestPodLogs(t *testing.T) {
	t.Run("streams the container with follow", func(t *testing.T) {
		fc := k8sfake.NewSimpleClientset()
		fc.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, &runtime.Unknown{Raw: []byte("hello\n")}, nil
		})
		c := &kubeClient{core: fc, tekton: tektonfake.NewSimpleClientset().TektonV1()}

		rc, err := c.PodLogs(context.Background(), "ns", "pod-1", "step-build")
		require.NoError(t, err)
		defer rc.Close() //nolint:errcheck // nothing to do with a closed test stream
		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, "hello\n", string(body))

		var logAction ktesting.Action
		for _, a := range fc.Actions() {
			if a.GetVerb() == "get" && a.GetSubresource() == "log" {
				logAction = a
			}
		}
		require.NotNil(t, logAction, "a pods/log get must have been issued")
		opts, ok := logAction.(ktesting.GenericAction).GetValue().(*corev1.PodLogOptions)
		require.True(t, ok, "the log action must carry PodLogOptions")
		assert.Equal(t, "step-build", opts.Container)
		assert.True(t, opts.Follow)
	})

	t.Run("wraps open errors", func(t *testing.T) {
		fc := k8sfake.NewSimpleClientset()
		fc.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("boom")
		})
		c := &kubeClient{core: fc, tekton: tektonfake.NewSimpleClientset().TektonV1()}

		_, err := c.PodLogs(context.Background(), "ns", "pod-1", "step-build")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "opening logs of pod-1/step-build")
	})
}

func TestDeleteNamespace(t *testing.T) {
	t.Run("deletes the namespace", func(t *testing.T) {
		c := newFakeKubeClient([]runtime.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "pact-run-x"}},
		}, nil)
		require.NoError(t, c.DeleteNamespace(context.Background(), "pact-run-x"))

		_, err := c.core.CoreV1().Namespaces().Get(context.Background(), "pact-run-x", metav1.GetOptions{})
		require.Error(t, err, "the namespace must be gone")
	})

	t.Run("a missing namespace is not an error", func(t *testing.T) {
		c := newFakeKubeClient(nil, nil)
		require.NoError(t, c.DeleteNamespace(context.Background(), "pact-run-x"))
	})
}

func TestSourcePod(t *testing.T) {
	pod := sourcePod("pact-source")

	assert.Equal(t, "pact-upload-", pod.GenerateName)
	require.Len(t, pod.Spec.Containers, 1)
	container := pod.Spec.Containers[0]
	assert.Equal(t, uploadImage, container.Image)
	require.Len(t, container.VolumeMounts, 1)
	assert.Equal(t, uploadMountPath, container.VolumeMounts[0].MountPath)
	require.Len(t, pod.Spec.Volumes, 1)
	require.NotNil(t, pod.Spec.Volumes[0].PersistentVolumeClaim)
	assert.Equal(t, "pact-source", pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName)
	require.NotNil(t, pod.Spec.TerminationGracePeriodSeconds)
	assert.Zero(t, *pod.Spec.TerminationGracePeriodSeconds, "the deferred delete must be immediate")
}

func TestPodRunning(t *testing.T) {
	tests := []struct {
		phase   corev1.PodPhase
		want    bool
		wantErr bool
	}{
		{phase: corev1.PodRunning, want: true},
		{phase: corev1.PodPending, want: false},
		{phase: corev1.PodSucceeded, wantErr: true},
		{phase: corev1.PodFailed, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
				Status:     corev1.PodStatus{Phase: tt.phase},
			}
			c := newFakeKubeClient([]runtime.Object{pod}, nil)
			got, err := c.podRunning(context.Background(), "ns", "p")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
