// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package poll

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitea.com/gitea/runner/internal/pkg/client/mocks"
	"gitea.com/gitea/runner/internal/pkg/config"

	runnerv1 "code.gitea.io/actions-proto-go/runner/v1"
	connect_go "connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestPoller_WorkerStateCounters verifies that workerState correctly tracks
// consecutive empty responses independently per state instance, and that
// fetchTask increments only the relevant counter.
func TestPoller_WorkerStateCounters(t *testing.T) {
	client := mocks.NewClient(t)
	client.On("FetchTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.FetchTaskRequest]) (*connect_go.Response[runnerv1.FetchTaskResponse], error) {
			// Always return an empty response.
			return connect_go.NewResponse(&runnerv1.FetchTaskResponse{}), nil
		},
	)

	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	p := &Poller{client: client, cfg: cfg}

	ctx := context.Background()
	s1 := &workerState{}
	s2 := &workerState{}

	// Each worker independently observes one empty response.
	_, ok := p.fetchTask(ctx, s1)
	require.False(t, ok)
	_, ok = p.fetchTask(ctx, s2)
	require.False(t, ok)

	assert.Equal(t, int64(1), s1.consecutiveEmpty, "worker 1 should only count its own empty response")
	assert.Equal(t, int64(1), s2.consecutiveEmpty, "worker 2 should only count its own empty response")

	// Worker 1 sees a second empty; worker 2 stays at 1.
	_, ok = p.fetchTask(ctx, s1)
	require.False(t, ok)
	assert.Equal(t, int64(2), s1.consecutiveEmpty)
	assert.Equal(t, int64(1), s2.consecutiveEmpty, "worker 2's counter must not be affected by worker 1's empty fetches")
}

// TestPoller_FetchErrorIncrementsErrorsOnly verifies that a fetch error
// increments only the per-worker error counter, not the empty counter.
func TestPoller_FetchErrorIncrementsErrorsOnly(t *testing.T) {
	client := mocks.NewClient(t)
	client.On("FetchTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.FetchTaskRequest]) (*connect_go.Response[runnerv1.FetchTaskResponse], error) {
			return nil, errors.New("network unreachable")
		},
	)

	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	p := &Poller{client: client, cfg: cfg}

	s := &workerState{}
	_, ok := p.fetchTask(context.Background(), s)
	require.False(t, ok)
	assert.Equal(t, int64(1), s.consecutiveErrors)
	assert.Equal(t, int64(0), s.consecutiveEmpty)
}

// TestPoller_CalculateInterval verifies the exponential backoff math is
// correctly driven by the workerState counters.
func TestPoller_CalculateInterval(t *testing.T) {
	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.Runner.FetchInterval = 2 * time.Second
	cfg.Runner.FetchIntervalMax = 60 * time.Second
	p := &Poller{cfg: cfg}

	cases := []struct {
		name         string
		empty, errs  int64
		wantInterval time.Duration
	}{
		{"first poll, no backoff", 0, 0, 2 * time.Second},
		{"single empty, still base", 1, 0, 2 * time.Second},
		{"two empties, doubled", 2, 0, 4 * time.Second},
		{"five empties, capped path", 5, 0, 32 * time.Second},
		{"many empties, capped at max", 20, 0, 60 * time.Second},
		{"errors drive backoff too", 0, 3, 8 * time.Second},
		{"max(empty, errors) wins", 2, 4, 16 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &workerState{consecutiveEmpty: tc.empty, consecutiveErrors: tc.errs}
			assert.Equal(t, tc.wantInterval, p.calculateInterval(s))
		})
	}
}

// atomicMax atomically updates target to max(target, val).
func atomicMax(target *atomic.Int64, val int64) {
	for {
		old := target.Load()
		if val <= old || target.CompareAndSwap(old, val) {
			break
		}
	}
}

type mockRunner struct {
	delay          time.Duration
	running        atomic.Int64
	maxConcurrent  atomic.Int64
	totalCompleted atomic.Int64
}

type idleAwareRunner struct {
	mockRunner
	idleCalls atomic.Int64
}

func (m *mockRunner) Run(ctx context.Context, _ *runnerv1.Task) error {
	atomicMax(&m.maxConcurrent, m.running.Add(1))
	select {
	case <-time.After(m.delay):
	case <-ctx.Done():
	}
	m.running.Add(-1)
	m.totalCompleted.Add(1)
	return nil
}

func TestPollerRunIdleMaintenance(t *testing.T) {
	runner := &idleAwareRunner{}
	p := &Poller{runner: runner, jobsCtx: context.Background()}

	p.runIdleMaintenance()

	assert.Equal(t, int64(1), runner.idleCalls.Load())
}

func (m *idleAwareRunner) OnIdle(_ context.Context) {
	m.idleCalls.Add(1)
}

func TestPollerPollCallsOnIdle(t *testing.T) {
	cli := mocks.NewClient(t)
	cli.On("FetchTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.FetchTaskRequest]) (*connect_go.Response[runnerv1.FetchTaskResponse], error) {
			return connect_go.NewResponse(&runnerv1.FetchTaskResponse{}), nil
		},
	)

	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.Runner.Capacity = 1
	cfg.Runner.FetchInterval = 10 * time.Millisecond
	cfg.Runner.FetchIntervalMax = 10 * time.Millisecond

	runner := &idleAwareRunner{}
	poller := New(cfg, cli, runner)

	var wg sync.WaitGroup
	wg.Go(poller.Poll)

	require.Eventually(t, func() bool {
		return runner.idleCalls.Load() > 0
	}, time.Second, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, poller.Shutdown(ctx))
	wg.Wait()
}

func TestPollerPollOnceCallsOnIdle(t *testing.T) {
	cli := mocks.NewClient(t)
	cli.On("FetchTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.FetchTaskRequest]) (*connect_go.Response[runnerv1.FetchTaskResponse], error) {
			return connect_go.NewResponse(&runnerv1.FetchTaskResponse{}), nil
		},
	)

	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.Runner.FetchInterval = 10 * time.Millisecond
	cfg.Runner.FetchIntervalMax = 10 * time.Millisecond

	runner := &idleAwareRunner{}
	poller := New(cfg, cli, runner)

	var wg sync.WaitGroup
	wg.Go(poller.PollOnce)

	require.Eventually(t, func() bool {
		return runner.idleCalls.Load() > 0
	}, time.Second, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, poller.Shutdown(ctx))
	wg.Wait()
}

// TestPoller_ConcurrencyLimitedByCapacity verifies that with capacity=3 and
// 6 available tasks, at most 3 tasks run concurrently, and FetchTask is
// never called concurrently (single poller).
func TestPoller_ConcurrencyLimitedByCapacity(t *testing.T) {
	const (
		capacity   = 3
		totalTasks = 6
		taskDelay  = 50 * time.Millisecond
	)

	var (
		tasksReturned  atomic.Int64
		fetchConcur    atomic.Int64
		maxFetchConcur atomic.Int64
	)

	cli := mocks.NewClient(t)
	cli.On("FetchTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.FetchTaskRequest]) (*connect_go.Response[runnerv1.FetchTaskResponse], error) {
			atomicMax(&maxFetchConcur, fetchConcur.Add(1))
			defer fetchConcur.Add(-1)

			n := tasksReturned.Add(1)
			if n <= totalTasks {
				return connect_go.NewResponse(&runnerv1.FetchTaskResponse{
					Task: &runnerv1.Task{Id: n},
				}), nil
			}
			return connect_go.NewResponse(&runnerv1.FetchTaskResponse{}), nil
		},
	)

	runner := &mockRunner{delay: taskDelay}

	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.Runner.Capacity = capacity
	cfg.Runner.FetchInterval = 10 * time.Millisecond
	cfg.Runner.FetchIntervalMax = 10 * time.Millisecond

	poller := New(cfg, cli, runner)

	var wg sync.WaitGroup
	wg.Go(poller.Poll)

	require.Eventually(t, func() bool {
		return runner.totalCompleted.Load() >= totalTasks
	}, 2*time.Second, 10*time.Millisecond, "all tasks should complete")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = poller.Shutdown(ctx)
	require.NoError(t, err)
	wg.Wait()

	assert.LessOrEqual(t, runner.maxConcurrent.Load(), int64(capacity),
		"concurrent running tasks must not exceed capacity")
	assert.GreaterOrEqual(t, runner.maxConcurrent.Load(), int64(2),
		"with 6 tasks and capacity 3, at least 2 should overlap")
	assert.Equal(t, int64(1), maxFetchConcur.Load(),
		"FetchTask must never be called concurrently (single poller)")
	assert.Equal(t, int64(totalTasks), runner.totalCompleted.Load(),
		"all tasks should have been executed")
}

// TestPoller_ShutdownForcesJobsOnTimeout locks in the fix for a
// pre-existing bug where Shutdown's timeout branch used a blocking
// `<-p.done` receive, leaving p.shutdownJobs() unreachable. With a
// task parked on jobsCtx and a Shutdown deadline shorter than the
// task's natural completion, Shutdown must force-cancel via
// shutdownJobs() and return ctx.Err() promptly — not block until the
// task would have finished on its own.
func TestPoller_ShutdownForcesJobsOnTimeout(t *testing.T) {
	var served atomic.Bool
	cli := mocks.NewClient(t)
	cli.On("FetchTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.FetchTaskRequest]) (*connect_go.Response[runnerv1.FetchTaskResponse], error) {
			if served.CompareAndSwap(false, true) {
				return connect_go.NewResponse(&runnerv1.FetchTaskResponse{
					Task: &runnerv1.Task{Id: 1},
				}), nil
			}
			return connect_go.NewResponse(&runnerv1.FetchTaskResponse{}), nil
		},
	)

	// delay >> Shutdown timeout: Run only returns when jobsCtx is
	// cancelled by shutdownJobs().
	runner := &mockRunner{delay: 30 * time.Second}

	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.Runner.Capacity = 1
	cfg.Runner.FetchInterval = 10 * time.Millisecond
	cfg.Runner.FetchIntervalMax = 10 * time.Millisecond

	poller := New(cfg, cli, runner)

	var wg sync.WaitGroup
	wg.Go(poller.Poll)

	require.Eventually(t, func() bool {
		return runner.running.Load() == 1
	}, time.Second, 10*time.Millisecond, "task should start running")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = poller.Shutdown(ctx)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	// With the fix, Shutdown returns shortly after the deadline once
	// the forced job unwinds. Without the fix, the blocking <-p.done
	// would hang for the full 30s mockRunner delay.
	assert.Less(t, elapsed, 5*time.Second,
		"Shutdown must not block on the parked task; shutdownJobs() must run on timeout")

	wg.Wait()
	assert.Equal(t, int64(1), runner.totalCompleted.Load(),
		"the parked task must be cancelled and unwound")
}
