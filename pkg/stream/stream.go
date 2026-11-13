// Package stream follows a PipelineRun's TaskRun logs live, prefixing
// every line with its task and step, until the run reaches a terminal
// state and all discovered logs have drained.
package stream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/tektoncd/pipeline/pkg/apis/pipeline"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
)

// Source is the read-only cluster access the streamer needs. It is
// satisfied by pkg/runner's client and by fakes in tests.
type Source interface {
	// GetPipelineRun returns the PipelineRun by name.
	GetPipelineRun(ctx context.Context, ns, name string) (*tektonv1.PipelineRun, error)
	// ListTaskRuns returns the TaskRuns labelled as children of the
	// named PipelineRun.
	ListTaskRuns(ctx context.Context, ns, pipelineRun string) ([]tektonv1.TaskRun, error)
	// PodLogs opens a following log stream for one container; it EOFs
	// when the container terminates.
	PodLogs(ctx context.Context, ns, pod, container string) (io.ReadCloser, error)
}

// Result is the terminal outcome of a followed PipelineRun.
type Result struct {
	// Success reports the PipelineRun's Succeeded condition.
	Success bool
	// Message is the PipelineRun's completion message.
	Message string
}

// Tunables kept as variables so tests can speed them up.
var (
	// pollInterval is how often the PipelineRun and its TaskRuns are
	// re-checked.
	pollInterval = time.Second
	// drainTimeout bounds the wait for log streams to EOF after the
	// PipelineRun went terminal.
	drainTimeout = 15 * time.Second
	// logRetries is how often opening a container's log stream is
	// retried before giving up with a notice.
	logRetries = 3
	// logRetryDelay backs off between log stream open attempts.
	logRetryDelay = 500 * time.Millisecond
)

// outcome reports whether a PipelineRun has reached a terminal state.
// Tekton publishes the Succeeded condition with Status Unknown while the
// run is in progress; only True or False are final.
func outcome(pr *tektonv1.PipelineRun) (done, success bool, message string) {
	for _, c := range pr.Status.Conditions {
		if c.Type != "Succeeded" {
			continue
		}
		switch c.Status {
		case corev1.ConditionTrue:
			return true, true, c.Message
		case corev1.ConditionFalse:
			return true, false, c.Message
		}
	}
	return false, false, ""
}

// Follow streams the logs of every TaskRun step of the named
// PipelineRun to out, prefixed "[task : step] ", and returns once the
// run is terminal and the streams have drained. A nil out discards the
// logs. Context cancellation aborts following and returns an error.
func Follow(ctx context.Context, src Source, ns, name string, out io.Writer) (Result, error) {
	f := &follower{
		src:     src,
		ns:      ns,
		run:     name,
		out:     &syncWriter{w: out},
		colors:  NewColors(out),
		started: map[string]struct{}{},
	}
	sctx, cancelStreams := context.WithCancel(ctx)
	defer cancelStreams()

	for {
		pr, err := src.GetPipelineRun(ctx, ns, name)
		switch {
		case err != nil && ctx.Err() != nil:
			return Result{}, fmt.Errorf("following pipelinerun %s: %w", name, ctx.Err())
		case err != nil:
			// A brief API-server blip or cache lag right after create
			// is retried on the next tick.
		default:
			done, success, message := outcome(pr)
			// This scan runs after the terminal read, so it sees every
			// step that existed when the run finished. It is best
			// effort: a failed tick is retried on the next one, and a
			// terminal run returns regardless.
			_ = f.scan(sctx)
			if done {
				t := time.AfterFunc(drainTimeout, cancelStreams)
				f.wg.Wait()
				t.Stop()
				return Result{Success: success, Message: message}, nil
			}
		}

		select {
		case <-ctx.Done():
			return Result{}, fmt.Errorf("following pipelinerun %s: %w", name, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// follower carries the state of one Follow call.
type follower struct {
	src     Source
	ns, run string
	out     *syncWriter
	colors  *Colors
	started map[string]struct{} // pod+"/"+container, only touched by Follow's goroutine
	wg      sync.WaitGroup
}

// scan lists the PipelineRun's TaskRuns and starts a stream goroutine
// for every step container that has started and is not already being
// followed.
func (f *follower) scan(ctx context.Context) error {
	taskRuns, err := f.src.ListTaskRuns(ctx, f.ns, f.run)
	if err != nil {
		return err
	}
	for i := range taskRuns {
		tr := &taskRuns[i]
		if tr.Status.PodName == "" {
			continue
		}
		task := tr.Labels[pipeline.PipelineTaskLabelKey]
		if task == "" {
			task = tr.Name
		}
		for _, step := range tr.Status.Steps {
			// Waiting steps have not started; skipped steps after a
			// failure stay Waiting and have no logs to fetch.
			if step.Container == "" || step.Waiting != nil {
				continue
			}
			key := tr.Status.PodName + "/" + step.Container
			if _, ok := f.started[key]; ok {
				continue
			}
			f.started[key] = struct{}{}
			prefix := f.colors.TaskPrefix(task, step.Name)
			f.wg.Add(1)
			go f.stream(ctx, tr.Status.PodName, step.Container, prefix)
		}
	}
	return nil
}

// stream follows one container's log to EOF, retrying the open while
// the kubelet is still catching up.
func (f *follower) stream(ctx context.Context, pod, container, prefix string) {
	defer f.wg.Done()
	var rc io.ReadCloser
	var err error
	for range logRetries {
		rc, err = f.src.PodLogs(ctx, f.ns, pod, container)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(logRetryDelay):
		}
	}
	if err != nil {
		if ctx.Err() == nil {
			fmt.Fprintf(f.out, "%s(logs unavailable: %v)\n", prefix, err)
		}
		return
	}
	defer rc.Close() //nolint:errcheck // best effort on a finished stream
	if err := copyLines(rc, prefix, f.out); err != nil && ctx.Err() == nil {
		fmt.Fprintf(f.out, "%s(logs unavailable: %v)\n", prefix, err)
	}
}

// copyLines copies r to out line by line, prefixing each line. A final
// partial line without a trailing newline is still emitted.
func copyLines(r io.Reader, prefix string, out io.Writer) error {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			// A line read with err == nil already ends in '\n'; only a
			// final partial line needs one appended.
			if line[len(line)-1] != '\n' {
				line += "\n"
			}
			fmt.Fprintf(out, "%s%s", prefix, line)
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err //nolint:wrapcheck // reported to the caller as a notice
		}
	}
}

// syncWriter serializes writes so parallel TaskRun streams never tear a
// line.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == nil {
		return len(p), nil
	}
	return s.w.Write(p) //nolint:wrapcheck // pass-through writer
}
