package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitForTektonReadyImmediately(t *testing.T) {
	// Zero polls needed: both deployments report Available on first probe.
	lister := &scriptedLister{availability: map[string]int{
		"tekton-pipelines-controller": 0,
		"tekton-pipelines-webhook":    0,
	}}
	require.NoError(t, waitForTekton(context.Background(), lister, nil))
	assert.ElementsMatch(t, tektonDeployments, lister.probed)
}

func TestWaitForTektonPollsUntilAvailable(t *testing.T) {
	oldInterval := deploymentPollInterval
	deploymentPollInterval = time.Millisecond
	t.Cleanup(func() { deploymentPollInterval = oldInterval })

	// controller is available from the start; webhook only after 2 polls.
	lister := &scriptedLister{availability: map[string]int{
		"tekton-pipelines-controller": 0,
		"tekton-pipelines-webhook":    2,
	}}
	require.NoError(t, waitForTekton(context.Background(), lister, nil))
	assert.GreaterOrEqual(t, lister.calls["tekton-pipelines-webhook"], 3,
		"the webhook deployment must be probed again after being unavailable")
}

func TestWaitForTektonTimesOut(t *testing.T) {
	oldInterval := deploymentPollInterval
	deploymentPollInterval = time.Millisecond
	t.Cleanup(func() { deploymentPollInterval = oldInterval })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	t.Cleanup(cancel)

	lister := &scriptedLister{availability: map[string]int{
		"tekton-pipelines-controller": 100, // never available
		"tekton-pipelines-webhook":    100,
	}}
	err := waitForTekton(ctx, lister, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "want DeadlineExceeded, got %v", err)
}

func TestWaitForTektonSurfacesListerError(t *testing.T) {
	lister := &failingLister{err: errors.New("api down")}
	err := waitForTekton(context.Background(), lister, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api down")
}

func TestWaitForTektonHeartbeat(t *testing.T) {
	oldPoll, oldHeartbeat := deploymentPollInterval, waitHeartbeatInterval
	deploymentPollInterval, waitHeartbeatInterval = time.Millisecond, time.Millisecond
	t.Cleanup(func() { deploymentPollInterval, waitHeartbeatInterval = oldPoll, oldHeartbeat })

	// Neither deployment becomes available for a few polls, so the
	// heartbeat must fire while the user is waiting.
	var beats []time.Duration
	lister := &scriptedLister{availability: map[string]int{
		"tekton-pipelines-controller": 3,
		"tekton-pipelines-webhook":    3,
	}}
	require.NoError(t, waitForTekton(context.Background(), lister, func(elapsed time.Duration) {
		beats = append(beats, elapsed)
	}))
	assert.NotEmpty(t, beats, "a slow readiness wait must report progress")
	for i := 1; i < len(beats); i++ {
		assert.GreaterOrEqual(t, beats[i], beats[i-1], "heartbeats must be monotonic")
	}
}

func TestWaitForTektonNoHeartbeatWhenFast(t *testing.T) {
	// The default heartbeat interval (15s) is far longer than this wait,
	// so a fast startup must stay silent.
	lister := &scriptedLister{availability: map[string]int{
		"tekton-pipelines-controller": 1,
		"tekton-pipelines-webhook":    1,
	}}
	var beats int
	require.NoError(t, waitForTekton(context.Background(), lister, func(time.Duration) { beats++ }))
	assert.Zero(t, beats)
}

// failingLister always fails IsAvailable.
type failingLister struct {
	err error
}

func (f *failingLister) IsAvailable(context.Context, string, string) (bool, error) {
	return false, f.err
}

// scriptedLister reports a deployment available after N polls, recording
// every probed name.
type scriptedLister struct {
	availability map[string]int // name -> polls needed before available
	calls        map[string]int
	probed       []string
}

func (s *scriptedLister) IsAvailable(_ context.Context, _, name string) (bool, error) {
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[name]++
	s.probed = append(s.probed, name)
	return s.calls[name] > s.availability[name], nil
}
