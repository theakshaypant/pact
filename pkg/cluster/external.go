package cluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
)

// pingTimeout bounds a single API reachability probe.
const pingTimeout = 10 * time.Second

// externalProvider connects to a pre-existing cluster (a laptop minikube, a
// remote cluster, a kind cluster somebody else owns) through its kubeconfig.
// pact never manages the cluster itself: Ensure only verifies the API is
// reachable, and Delete removes just pact's Tekton installation.
type externalProvider struct {
	// kubeconfigPath is an explicit kubeconfig path; empty discovers one
	// from $KUBECONFIG, then the user's default kubeconfig.
	kubeconfigPath string

	// kubeconfig resolves the kubeconfig and returns its path (for
	// messages) and its content (for clients).
	kubeconfig func() (path string, content []byte, err error)
	ping       func(ctx context.Context, content []byte) error
	newClients clientsFactory
}

func newExternalProvider(kubeconfigPath string, newClients clientsFactory) *externalProvider {
	return &externalProvider{
		kubeconfigPath: kubeconfigPath,
		kubeconfig: func() (string, []byte, error) {
			if kubeconfigPath != "" {
				content, err := os.ReadFile(kubeconfigPath)
				if err != nil {
					return kubeconfigPath, nil, fmt.Errorf("reading kubeconfig %s: %w", kubeconfigPath, err)
				}
				return kubeconfigPath, content, nil
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return "", nil, fmt.Errorf("determining home directory: %w", err)
			}
			path, err := discoverKubeconfig(os.Getenv, home)
			if err != nil {
				return "", nil, err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return path, nil, fmt.Errorf("reading kubeconfig %s: %w", path, err)
			}
			return path, content, nil
		},
		ping:       pingAPI,
		newClients: newClients,
	}
}

func (p *externalProvider) Describe() string {
	if p.kubeconfigPath != "" {
		return fmt.Sprintf("external cluster (%s)", p.kubeconfigPath)
	}
	return "external cluster"
}

func (p *externalProvider) Removed() string {
	return "tekton installation on the external cluster"
}

func (p *externalProvider) Ensure(ctx context.Context) (bool, error) {
	path, content, err := p.kubeconfig()
	if err != nil {
		return false, err
	}
	if err := p.ping(ctx, content); err != nil {
		return false, fmt.Errorf("external cluster API unreachable via %s (is the cluster running and reachable?): %w", path, err)
	}
	return false, nil
}

func (p *externalProvider) Kubeconfig(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, content, err := p.kubeconfig()
	return content, err
}

// Delete removes pact's Tekton installation. A Tekton installation pact
// did not apply is left in place unless force is set: it belongs to the
// cluster's owner, not to pact.
func (p *externalProvider) Delete(ctx context.Context, force bool) error {
	path, content, err := p.kubeconfig()
	if err != nil {
		return err
	}
	objectClient, _, err := p.newClients(content)
	if err != nil {
		return fmt.Errorf("building kubernetes clients from %s: %w", path, err)
	}
	if !force {
		applied, err := tektonAppliedByPact(ctx, objectClient)
		if err != nil {
			return fmt.Errorf("checking whether pact installed tekton: %w", err)
		}
		if !applied {
			return fmt.Errorf("tekton on this cluster was not installed by pact; leaving it in place (pass --force to remove it)")
		}
	}
	return uninstallTekton(ctx, objectClient)
}

// discoverKubeconfig finds a kubeconfig for an external cluster: the first
// entry of $KUBECONFIG (a list merges in order, so the first file wins for
// the current context), then the user's default kubeconfig. Candidates that
// exist but cannot be opened are skipped so the next candidate gets a
// chance.
func discoverKubeconfig(getenv func(string) string, home string) (string, error) {
	var candidates []string
	if v := getenv("KUBECONFIG"); v != "" {
		if parts := strings.SplitN(v, string(os.PathListSeparator), 2); len(parts) > 0 && parts[0] != "" {
			candidates = append(candidates, parts[0])
		}
	}
	candidates = append(candidates, filepath.Join(home, ".kube", "config"))

	for _, c := range candidates {
		if st, err := os.Stat(c); err != nil || st.IsDir() {
			continue
		}
		f, err := os.Open(c) // verify it is actually readable
		if err != nil {
			continue
		}
		_ = f.Close()
		return c, nil
	}
	return "", fmt.Errorf("no kubeconfig found (tried %s); pass --kubeconfig, set $KUBECONFIG, or pass --provider kind to let pact create its own cluster", strings.Join(candidates, ", "))
}

// pingAPI checks that the API server behind the kubeconfig content
// answers a version request.
func pingAPI(ctx context.Context, content []byte) error {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(content)
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	cfg.Timeout = pingTimeout
	client, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building discovery client: %w", err)
	}
	// The request itself carries the context (ServerVersion does not),
	// so cancellation interrupts the probe instead of only being
	// reported afterwards.
	if err := client.RESTClient().Get().AbsPath("/version").Do(ctx).Error(); err != nil {
		return fmt.Errorf("querying server version: %w", err)
	}
	return nil
}
