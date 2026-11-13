package cluster

import (
	"context"
	"fmt"
	"io"
	"time"

	kind "sigs.k8s.io/kind/pkg/cluster"
)

// kindReadyTimeout bounds how long a fresh kind node may take to reach
// readiness inside Provider.Create. kind's API predates context
// propagation, so this is the only bound on cluster creation.
const kindReadyTimeout = 5 * time.Minute

// kindProvider manages a kind cluster whose full lifecycle pact owns.
// The kind operations are injectable so the provider logic is testable
// without Docker.
type kindProvider struct {
	name string

	list       func() ([]string, error)
	create     func(name string, opts ...kind.CreateOption) error
	delete     func(name, explicitKubeconfigPath string) error
	kubeconfig func(name string, internal bool) (string, error)
}

// newKindProvider wires the kind Go library. Progress messages kind
// emits during cluster creation are forwarded to progress (nil disables
// them), so `pact up` shows live status instead of going quiet.
func newKindProvider(name string, progress io.Writer) *kindProvider {
	p := kind.NewProvider(kind.ProviderWithLogger(newKindLogger(progress)))
	return &kindProvider{
		name:       name,
		list:       p.List,
		create:     p.Create,
		delete:     p.Delete,
		kubeconfig: p.KubeConfig,
	}
}

func (p *kindProvider) Describe() string {
	return fmt.Sprintf("kind cluster %q", p.name)
}

func (p *kindProvider) Removed() string {
	return p.Describe()
}

func (p *kindProvider) Ensure(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	clusters, err := p.list()
	if err != nil {
		// kind reaches Docker through its CLI; a failure here almost
		// always means the daemon is not running.
		return false, fmt.Errorf("listing kind clusters (is the docker daemon running?): %w", err)
	}
	for _, c := range clusters {
		if c == p.name {
			return false, nil
		}
	}
	if err := p.create(p.name, kind.CreateWithWaitForReady(kindReadyTimeout)); err != nil {
		return false, fmt.Errorf("creating %s: %w", p.Describe(), err)
	}
	return true, nil
}

func (p *kindProvider) Kubeconfig(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// internal=false: the kubeconfig points at the host-mapped API server
	// port, so kubectl on the host works with it too. kind returns the
	// kubeconfig content, not a path.
	kc, err := p.kubeconfig(p.name, false)
	if err != nil {
		return nil, fmt.Errorf("reading kind kubeconfig for %q: %w", p.name, err)
	}
	return []byte(kc), nil
}

// Delete removes the whole kind cluster; force is irrelevant because
// nothing survives a cluster deletion.
func (p *kindProvider) Delete(ctx context.Context, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// An empty explicit kubeconfig path lets kind use the default
	// kubeconfig, which is also where it wrote pact-local's entry.
	if err := p.delete(p.name, ""); err != nil {
		return fmt.Errorf("deleting %s: %w", p.Describe(), err)
	}
	return nil
}
