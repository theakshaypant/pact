package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tektoncd/pipeline/pkg/apis/pipeline"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	tektonclient "github.com/tektoncd/pipeline/pkg/client/clientset/versioned/typed/pipeline/v1"
	"github.com/theakshaypant/pact/pkg/stream"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

// Client is the Kubernetes interface the runner drives. It abstracts
// client-go so tests can fake the cluster entirely. The embedded
// stream.Source exposes the run's live state to the log streamer.
type Client interface {
	stream.Source
	// EnsureNamespace creates a namespace, tolerating that it exists.
	EnsureNamespace(ctx context.Context, name string) error
	// CreatePVC creates the source workspace claim.
	CreatePVC(ctx context.Context, ns, name, size string) error
	// UploadSource streams a gzipped tar archive into the PVC pvcName
	// in namespace ns, untarring it at the volume root via a short-lived
	// pod.
	UploadSource(ctx context.Context, ns, pvcName string, archive io.Reader) error
	// Submit creates the PipelineRun and returns the name it was created
	// under (the server derives one from generateName).
	Submit(ctx context.Context, pr *tektonv1.PipelineRun) (name string, err error)
	// DeleteNamespace removes a namespace and everything in it.
	DeleteNamespace(ctx context.Context, name string) error
}

// NewClient builds the real client-go implementation from kubeconfig
// content.
func NewClient(kubeconfig []byte) (Client, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building kubernetes clientset: %w", err)
	}
	tekton, err := tektonclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building tekton clientset: %w", err)
	}
	return &kubeClient{config: cfg, core: clientset, tekton: tekton}, nil
}

// kubeClient implements Client on client-go.
type kubeClient struct {
	config *rest.Config
	core   kubernetes.Interface
	tekton tektonclient.TektonV1Interface
}

// uploadImage is the image of the source-upload pod. busybox is ~5MB and
// its tar applet is all the pod needs.
const uploadImage = "busybox:1.36"

// uploadMountPath is where the source PVC is mounted in the upload pod,
// matching where Tekton mounts a PipelineRun workspace.
const uploadMountPath = "/workspace"

// podRunningTimeout bounds waiting for the upload pod to start.
const podRunningTimeout = 2 * time.Minute

// podPollInterval is how often namespace and upload-pod readiness is
// re-checked.
var podPollInterval = 2 * time.Second

// saReadyTimeout bounds waiting for the namespace's default
// ServiceAccount. A fresh namespace's SA is created asynchronously by
// the controller; creating the upload pod before it exists is rejected
// by the ServiceAccount admission plugin.
var saReadyTimeout = 30 * time.Second

func (k *kubeClient) EnsureNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	_, err := k.core.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %s: %w", name, err)
	}
	// Wait until the default ServiceAccount exists so pod creation in
	// the fresh namespace cannot lose the race with the SA controller.
	err = wait.PollUntilContextTimeout(ctx, podPollInterval, saReadyTimeout, true,
		func(ctx context.Context) (bool, error) {
			_, err := k.core.CoreV1().ServiceAccounts(name).Get(ctx, "default", metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil {
				return false, fmt.Errorf("waiting for default serviceaccount in %s: %w", name, err)
			}
			return true, nil
		})
	if err != nil {
		return fmt.Errorf("waiting for namespace %s to be ready: %w", name, err)
	}
	return nil
}

func (k *kubeClient) CreatePVC(ctx context.Context, ns, name, size string) error {
	quantity, err := resource.ParseQuantity(size)
	if err != nil {
		return fmt.Errorf("parsing workspace size %q (want e.g. 1Gi): %w", size, err)
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: quantity},
			},
		},
	}
	_, err = k.core.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating pvc %s in %s: %w", name, ns, err)
	}
	return nil
}

func (k *kubeClient) UploadSource(ctx context.Context, ns, pvcName string, archive io.Reader) error {
	created, err := k.core.CoreV1().Pods(ns).Create(ctx, sourcePod(pvcName), metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating source upload pod in %s: %w", ns, err)
	}
	defer func() {
		_ = k.core.CoreV1().Pods(ns).Delete(context.WithoutCancel(ctx), created.Name, metav1.DeleteOptions{})
	}()

	if err := wait.PollUntilContextTimeout(ctx, podPollInterval, podRunningTimeout, true,
		func(ctx context.Context) (bool, error) {
			return k.podRunning(ctx, ns, created.Name)
		}); err != nil {
		return fmt.Errorf("waiting for source upload pod: %w", err)
	}

	// Untar the streamed archive at the volume root.
	if err := k.exec(ctx, ns, created.Name, []string{"tar", "-xzf", "-", "-C", uploadMountPath}, archive); err != nil {
		return fmt.Errorf("streaming source into %s: %w", pvcName, err)
	}
	return nil
}

// sourcePod builds the short-lived pod that mounts the source PVC and
// untars the streamed archive into it.
func sourcePod(pvcName string) *corev1.Pod {
	grace := int64(0)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "pact-upload-"},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			// The container is `sleep`, which ignores SIGTERM; a zero
			// grace period makes the deferred delete immediate.
			TerminationGracePeriodSeconds: &grace,
			Containers: []corev1.Container{{
				Name:  "upload",
				Image: uploadImage,
				// Keep the pod alive for the exec; it is deleted right
				// after the stream finishes.
				Command: []string{"sleep", "3600"},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "source",
					MountPath: uploadMountPath,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "source",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName},
				},
			}},
		},
	}
}

func (k *kubeClient) podRunning(ctx context.Context, ns, name string) (bool, error) {
	pod, err := k.core.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err //nolint:wrapcheck // poll loop wraps or surfaces this
	}
	switch pod.Status.Phase {
	case corev1.PodRunning:
		return true, nil
	case corev1.PodFailed, corev1.PodSucceeded:
		return false, fmt.Errorf("source upload pod %s finished unexpectedly: %s", name, pod.Status.Reason)
	default:
		return false, nil
	}
}

// exec runs a command in the pod over the SPDY exec subresource,
// streaming stdin into it and capturing stderr for error reporting.
func (k *kubeClient) exec(ctx context.Context, ns, pod string, command []string, stdin io.Reader) error {
	restClient := k.core.CoreV1().RESTClient()
	if restClient == nil {
		return fmt.Errorf("kubernetes client has no REST client configured")
	}
	req := restClient.Post().
		Resource("pods").
		Namespace(ns).
		Name(pod).
		SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Command: command,
		Stdin:   true,
		Stderr:  true, // must match StreamOptions below: the server only creates requested streams
	}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(k.config, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("building exec request: %w", err)
	}
	var stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stderr: &stderr})
	if err != nil && stderr.Len() > 0 {
		return fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return err //nolint:wrapcheck // poll-free call, error is already contextual
}

func (k *kubeClient) Submit(ctx context.Context, pr *tektonv1.PipelineRun) (string, error) {
	created, err := k.tekton.PipelineRuns(pr.Namespace).Create(ctx, pr, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating pipelinerun in %s: %w", pr.Namespace, err)
	}
	return created.Name, nil
}

// GetPipelineRun returns the PipelineRun by name.
func (k *kubeClient) GetPipelineRun(ctx context.Context, ns, name string) (*tektonv1.PipelineRun, error) {
	pr, err := k.tekton.PipelineRuns(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting pipelinerun %s in %s: %w", name, ns, err)
	}
	return pr, nil
}

// ListTaskRuns returns the TaskRuns labelled as children of the named
// PipelineRun.
func (k *kubeClient) ListTaskRuns(ctx context.Context, ns, pipelineRun string) ([]tektonv1.TaskRun, error) {
	list, err := k.tekton.TaskRuns(ns).List(ctx, metav1.ListOptions{
		LabelSelector: pipeline.PipelineRunLabelKey + "=" + pipelineRun,
	})
	if err != nil {
		return nil, fmt.Errorf("listing taskruns of pipelinerun %s in %s: %w", pipelineRun, ns, err)
	}
	return list.Items, nil
}

// PodLogs opens a following log stream for one container; it EOFs when
// the container terminates.
func (k *kubeClient) PodLogs(ctx context.Context, ns, pod, container string) (io.ReadCloser, error) {
	rc, err := k.core.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{
		Container: container,
		Follow:    true,
	}).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening logs of %s/%s in %s: %w", pod, container, ns, err)
	}
	return rc, nil
}

// DeleteNamespace removes a namespace and everything in it. Deleting a
// namespace that is already gone is not an error.
func (k *kubeClient) DeleteNamespace(ctx context.Context, name string) error {
	err := k.core.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting namespace %s: %w", name, err)
	}
	return nil
}
