// Package cluster manages the Kubernetes clusters pact runs pipelines
// on. Two providers are supported: kind (a Docker-backed cluster whose
// full lifecycle pact owns) and external (any pre-existing cluster pact
// connects to via its kubeconfig without managing its lifecycle).
package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Provider names accepted by Options.Provider.
const (
	ProviderKind     = "kind"
	ProviderExternal = "external"
)

// DefaultName is the name of pact's kind cluster.
const DefaultName = "pact-local"

// Provider abstracts the cluster runtime pact talks to.
type Provider interface {
	// Describe names the cluster for progress messages, e.g.
	// `kind cluster "pact-local"`.
	Describe() string
	// Removed names what Delete removed, for the completion message,
	// e.g. `kind cluster "pact-local"` or `tekton installation on
	// microshift`.
	Removed() string
	// Ensure makes the cluster usable and reports whether it was created
	// by this call. kind creates the cluster when missing; microshift
	// only verifies the host install is reachable.
	Ensure(ctx context.Context) (created bool, err error)
	// Kubeconfig returns the content of a kubeconfig for the cluster.
	Kubeconfig(ctx context.Context) ([]byte, error)
	// Delete tears down what pact manages: the whole kind cluster, or
	// only pact's Tekton installation on the external cluster. force
	// removes a Tekton installation pact did not apply; providers that
	// delete a whole cluster ignore it.
	Delete(ctx context.Context, force bool) error
}

// Manager is the facade the CLI drives: it ensures a cluster, installs
// Tekton on it and waits for readiness.
type Manager struct {
	provider   Provider
	newClients clientsFactory
	progress   io.Writer
}

// Options configures New.
type Options struct {
	// Provider selects the cluster runtime: ProviderExternal (the
	// default), ProviderKind, or empty for external.
	Provider string
	// Kubeconfig is an explicit kubeconfig path for the external provider;
	// empty discovers one from $KUBECONFIG, then the user's default
	// kubeconfig. It is invalid with any other provider.
	Kubeconfig string
	// Progress receives progress messages; nil disables them.
	Progress io.Writer
}

// New returns a Manager for the selected provider.
func New(opts Options) (*Manager, error) {
	var p Provider
	switch opts.Provider {
	case "", ProviderExternal:
		p = newExternalProvider(opts.Kubeconfig, newKubeClients)
	case ProviderKind:
		if opts.Kubeconfig != "" {
			return nil, fmt.Errorf("a kubeconfig path applies only to the %s provider", ProviderExternal)
		}
		p = newKindProvider(DefaultName, opts.Progress)
	default:
		return nil, fmt.Errorf("unknown cluster provider %q (want %q or %q)",
			opts.Provider, ProviderKind, ProviderExternal)
	}
	return &Manager{provider: p, newClients: newKubeClients, progress: opts.Progress}, nil
}

// Kubeconfig returns the content of a kubeconfig for the provider's
// cluster, so other components (e.g. the runner) can build their own
// clients from the same connection pact manages.
func (m *Manager) Kubeconfig(ctx context.Context) ([]byte, error) {
	return m.provider.Kubeconfig(ctx)
}

// UpOptions configure Manager.Up.
type UpOptions struct {
	// TektonVersion installs this official Tekton Pipeline release
	// (e.g. "v1.19.0") instead of the embedded one. Fetching requires
	// network access.
	TektonVersion string
}

// Up ensures the cluster exists, ensures Tekton is installed on it and
// waits until the Tekton controllers are ready. It is idempotent: an
// existing cluster is reused, and a Tekton installation that is already
// present is left untouched (only verified) unless TektonVersion
// explicitly asks pact to install a specific release.
func (m *Manager) Up(ctx context.Context, opts UpOptions) error {
	// Resolve the manifest first: a bad --tekton-version or a failed
	// fetch must not leave a freshly created cluster behind.
	manifest, source, err := tektonManifest(ctx, opts.TektonVersion)
	if err != nil {
		return err
	}

	m.printf("ensuring %s\n", m.provider.Describe())
	created, err := m.provider.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("ensuring %s: %w", m.provider.Describe(), err)
	}
	verb := "reusing"
	if created {
		verb = "created"
	}
	m.printf("%s %s\n", verb, m.provider.Describe())

	kubeconfig, err := m.provider.Kubeconfig(ctx)
	if err != nil {
		return fmt.Errorf("locating kubeconfig for %s: %w", m.provider.Describe(), err)
	}

	objectClient, deployments, err := m.newClients(kubeconfig)
	if err != nil {
		return fmt.Errorf("building kubernetes clients: %w", err)
	}

	install := true
	if opts.TektonVersion == "" {
		installed, servesV1, err := tektonInstalled(ctx, objectClient)
		if err != nil {
			return fmt.Errorf("checking for an existing tekton installation: %w", err)
		}
		if installed {
			if !servesV1 {
				return errors.New("existing tekton installation does not serve tekton.dev/v1 (required by pact); upgrade it, or pass --tekton-version to install a specific release")
			}
			m.printf("reusing existing tekton installation\n")
			install = false
		}
	}
	if install {
		m.printf("installing tekton pipelines (%s)\n", source)
		if err := installTekton(ctx, objectClient, manifest); err != nil {
			return fmt.Errorf("installing tekton: %w", err)
		}
	}

	m.printf("waiting for tekton to become ready\n")
	heartbeat := func(elapsed time.Duration) {
		m.printf("still waiting for tekton to become ready (%.0fs elapsed)\n", elapsed.Seconds())
	}
	if err := waitForTekton(ctx, deployments, heartbeat); err != nil {
		return fmt.Errorf("waiting for tekton readiness: %w", err)
	}
	return nil
}

// DownOptions configure Manager.Down.
type DownOptions struct {
	// Force removes a Tekton installation that pact did not apply
	// (external provider only; kind deletes the whole cluster anyway).
	Force bool
}

// Down removes what pact manages: the whole kind cluster, or only
// pact's Tekton installation on the external cluster — refusing to
// remove a Tekton installation pact did not apply unless forced.
func (m *Manager) Down(ctx context.Context, opts DownOptions) error {
	if err := m.provider.Delete(ctx, opts.Force); err != nil {
		return fmt.Errorf("tearing down %s: %w", m.provider.Describe(), err)
	}
	m.printf("removed %s\n", m.provider.Removed())
	return nil
}

// printf writes a progress message if a progress writer is set.
func (m *Manager) printf(format string, args ...any) {
	if m.progress != nil {
		fmt.Fprintf(m.progress, format, args...)
	}
}
