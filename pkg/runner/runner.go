// Package runner executes resolved PipelineRuns against the local
// cluster: it packages the local source, ships it into an isolated
// namespace and submits each PipelineRun with its workspace bound to
// that source.
package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"github.com/theakshaypant/pact/pkg/stream"
)

// pvcName is the name of the source claim created in every run
// namespace.
const pvcName = "pact-source"

// DefaultWorkspaceSize is the size of the source PVC unless overridden.
const DefaultWorkspaceSize = "1Gi"

// ClientFactory builds a Client from kubeconfig content; injectable so
// tests can fake the cluster entirely.
type ClientFactory func(kubeconfig []byte) (Client, error)

// Options configure Runner.Run.
type Options struct {
	// RepoRoot is the repository whose working tree is packaged as
	// source.
	RepoRoot string
	// Workspace binds the source PVC to this pipeline workspace instead
	// of the "source" convention.
	Workspace string
	// WorkspaceSize is the source PVC size (default 1Gi).
	WorkspaceSize string
	// KeepCloneTasks disables dropping repository-fetching tasks.
	KeepCloneTasks bool
	// NoCleanup keeps the run namespace even when every run succeeded.
	NoCleanup bool
	// Secrets are created in the run namespace before the runs are
	// submitted, so manifests can consume them by name.
	Secrets Secrets
}

// PipelineRunResult is the outcome of one executed PipelineRun.
type PipelineRunResult struct {
	// Name identifies the executed PipelineRun.
	Name string
	// Success reports the PipelineRun's Succeeded condition.
	Success bool
	// Message is the PipelineRun's completion message.
	Message string
}

// Result is the outcome of a Runner.Run call.
type Result struct {
	// Runs are the outcomes of each executed PipelineRun.
	Runs []PipelineRunResult
	// Namespace is the namespace the runs executed in.
	Namespace string
	// Cleaned reports whether the namespace was deleted afterwards.
	Cleaned bool
}

// FailedCount returns how many PipelineRuns failed.
func (r Result) FailedCount() int {
	var failed int
	for _, run := range r.Runs {
		if !run.Success {
			failed++
		}
	}
	return failed
}

// Runner executes resolved PipelineRuns against a cluster.
type Runner struct {
	newClient ClientFactory
	progress  io.Writer
	colors    *stream.Colors
}

// New returns a Runner building its cluster clients via newClient and
// writing progress to progress (nil disables it).
func New(newClient ClientFactory, progress io.Writer) *Runner {
	return &Runner{newClient: newClient, progress: progress, colors: stream.NewColors(progress)}
}

// Run executes every PipelineRun in an isolated namespace: it packages
// the repository's working tree into a PVC, mutates each PipelineRun to
// bind that PVC as its source workspace, submits it and waits for
// completion.
func (r *Runner) Run(ctx context.Context, kubeconfig []byte, runs []*tektonv1.PipelineRun, opts Options) (res Result, err error) {
	if len(runs) == 0 {
		return Result{}, fmt.Errorf("no pipelineruns to execute")
	}
	if opts.WorkspaceSize == "" {
		opts.WorkspaceSize = DefaultWorkspaceSize
	}

	// The unsupplied-reference check is pure, so it warns before any
	// cluster work: a pod referencing a missing secret would hang until
	// the timeout.
	if missing := opts.Secrets.missing(runs); len(missing) > 0 {
		r.printf("warning: pipelines reference secret(s) not supplied with --secret/--secret-file: %s "+
			"(pods hang in CreateContainerConfigError without them)\n", strings.Join(missing, ", "))
	}

	client, err := r.newClient(kubeconfig)
	if err != nil {
		return Result{}, fmt.Errorf("building kubernetes clients: %w", err)
	}

	ns, err := runNamespace()
	if err != nil {
		return res, err
	}
	if err := client.EnsureNamespace(ctx, ns); err != nil {
		return res, fmt.Errorf("preparing namespace %s: %w", ns, err)
	}
	res.Namespace = ns
	// submitted flips once the first PipelineRun is created: only then
	// is the namespace worth keeping for inspection on error.
	submitted := false
	defer func() {
		// The cleanup context must outlive a cancelled run context so
		// Ctrl-C still cleans up on the paths that allow it.
		r.finish(context.WithoutCancel(ctx), client, ns, &res, err, submitted, opts)
	}()

	if err := client.CreatePVC(ctx, ns, pvcName, opts.WorkspaceSize); err != nil {
		return res, fmt.Errorf("preparing source volume: %w", err)
	}
	if err := r.createSecrets(ctx, client, ns, opts.Secrets); err != nil {
		return res, err
	}

	r.printf("packaging source from %s\n", opts.RepoRoot)
	var archive bytes.Buffer
	files, err := packageSource(ctx, opts.RepoRoot, &archive)
	if err != nil {
		return res, fmt.Errorf("packaging source: %w", err)
	}
	r.printf("uploading source (%d files, %s)\n", files, byteCount(archive.Len()))
	if err := client.UploadSource(ctx, ns, pvcName, &archive); err != nil {
		return res, fmt.Errorf("uploading source: %w", err)
	}

	for i, pr := range runs {
		run, err := r.runOne(ctx, client, pr, ns, opts, i, len(runs), &submitted)
		if err != nil {
			return res, err
		}
		res.Runs = append(res.Runs, run)
	}
	return res, nil
}

// createSecrets creates the supplied secrets in the run namespace,
// sorted by name. Progress names the secret and its key count but never
// a value.
func (r *Runner) createSecrets(ctx context.Context, client Client, ns string, secrets Secrets) error {
	for _, name := range slices.Sorted(maps.Keys(secrets)) {
		if err := client.CreateSecret(ctx, ns, name, secrets[name]); err != nil {
			return err
		}
		r.printf("created secret %s (%d keys)\n", name, len(secrets[name]))
	}
	return nil
}

// finish applies the namespace cleanup policy after the runs: the
// namespace is deleted when nothing was submitted (a failed setup
// leaves nothing to inspect) or when every run succeeded, unless
// --no-cleanup was passed. Otherwise it is kept for inspection.
func (r *Runner) finish(ctx context.Context, client Client, ns string, res *Result, runErr error,
	submitted bool, opts Options,
) {
	if !opts.NoCleanup && (!submitted || (runErr == nil && res.FailedCount() == 0)) {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := client.DeleteNamespace(cctx, ns); err != nil {
			r.printf("warning: cleaning up namespace %s: %v\n", ns, err)
			return
		}
		res.Cleaned = true
		r.printf("cleaned up namespace %s\n", ns)
		return
	}
	r.printf("namespace %s kept for inspection: kubectl -n %s get pipelineruns,taskruns,pods "+
		"(remove: kubectl delete namespace %s)\n", ns, ns, ns)
}

// runOne mutates, submits and follows a single PipelineRun.
func (r *Runner) runOne(ctx context.Context, client Client, pr *tektonv1.PipelineRun, ns string,
	opts Options, i, total int, submitted *bool) (PipelineRunResult, error) {
	display := pr.Name
	if display == "" {
		display = strings.TrimSuffix(pr.GenerateName, "-")
	}
	r.printf("run %d/%d: %s\n", i+1, total, display)

	m, err := mutate(pr, opts.Workspace, opts.KeepCloneTasks)
	if err != nil {
		return PipelineRunResult{}, fmt.Errorf("preparing pipelinerun %s: %w", display, err)
	}
	for _, dropped := range m.DroppedTasks {
		r.printf("  dropped clone task %q (source is pre-populated)\n", dropped)
	}
	for _, dropped := range m.DroppedSteps {
		r.printf("  dropped clone step %q (source is pre-populated)\n", dropped)
	}
	bindWorkspace(pr, m.Workspace, pvcName)
	r.printf("  bound workspace %q to pvc %s\n", m.Workspace, pvcName)

	pr.Namespace = ns
	name, err := client.Submit(ctx, pr)
	if err != nil {
		return PipelineRunResult{}, err
	}
	*submitted = true
	r.printf("  submitted as %s; streaming logs\n", name)

	out, err := stream.Follow(ctx, client, ns, name, r.progress)
	if err != nil {
		return PipelineRunResult{}, fmt.Errorf("following pipelinerun %s: %w", name, err)
	}
	verb := r.colors.Green("succeeded")
	if !out.Success {
		verb = r.colors.Red("failed")
	}
	r.printf("  %s\n", verb)
	return PipelineRunResult{
		Name:    name,
		Success: out.Success,
		Message: out.Message,
	}, nil
}

// runNamespace returns a fresh run namespace name, pact-run-<random>.
func runNamespace() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating run namespace suffix: %w", err)
	}
	return "pact-run-" + hex.EncodeToString(b[:]), nil
}

// printf writes a progress message if a progress writer is set.
func (r *Runner) printf(format string, args ...any) {
	if r.progress != nil {
		fmt.Fprintf(r.progress, format, args...)
	}
}

// byteCount renders a byte count for humans.
func byteCount(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
