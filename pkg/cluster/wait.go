package cluster

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	// tektonNamespace is where the Tekton controllers run.
	tektonNamespace = "tekton-pipelines"
)

var (
	// deploymentPollInterval is how often Tekton readiness is
	// re-checked. A variable so tests can speed it up.
	deploymentPollInterval = 2 * time.Second

	// waitHeartbeatInterval is how often the readiness wait reports
	// that it is still working. A variable so tests can speed it up.
	waitHeartbeatInterval = 15 * time.Second
)

// tektonDeployments are the deployments that must be Available before
// Tekton can serve a PipelineRun.
var tektonDeployments = []string{"tekton-pipelines-controller", "tekton-pipelines-webhook"}

// deploymentLister reports whether a Deployment is Available; it
// abstracts the typed client so tests can fake it.
type deploymentLister interface {
	IsAvailable(ctx context.Context, namespace, name string) (bool, error)
}

// deploymentClient implements deploymentLister on a typed clientset.
type deploymentClient struct {
	client kubernetes.Interface
}

func (d *deploymentClient) IsAvailable(ctx context.Context, namespace, name string) (bool, error) {
	dep, err := d.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil // not created yet, keep waiting
		}
		return false, err
	}
	for _, c := range dep.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return true, nil
		}
	}
	return false, nil
}

// waitForTekton polls until every Tekton Deployment reports Available.
// While it waits, heartbeat (if set) is called with the elapsed time
// every waitHeartbeatInterval so the user sees the wait is alive.
func waitForTekton(ctx context.Context, lister deploymentLister, heartbeat func(elapsed time.Duration)) error {
	start := time.Now()
	var lastHeartbeat time.Duration
	return wait.PollUntilContextCancel(ctx, deploymentPollInterval, true, func(ctx context.Context) (bool, error) {
		for _, name := range tektonDeployments {
			available, err := lister.IsAvailable(ctx, tektonNamespace, name)
			if err != nil {
				return false, err
			}
			if !available {
				elapsed := time.Since(start)
				if heartbeat != nil && elapsed-lastHeartbeat >= waitHeartbeatInterval {
					lastHeartbeat = elapsed
					heartbeat(elapsed)
				}
				return false, nil
			}
		}
		return true, nil
	})
}
