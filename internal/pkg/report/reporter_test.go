// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package report

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitea.com/gitea/runner/internal/pkg/client/mocks"
	"gitea.com/gitea/runner/internal/pkg/config"

	runnerv1 "code.gitea.io/actions-proto-go/runner/v1"
	connect_go "connectrpc.com/connect"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestReporter_parseLogRow(t *testing.T) {
	tests := []struct {
		name               string
		debugOutputEnabled bool
		args               []string
		want               []string
	}{
		{
			"No command", false,
			[]string{"Hello, world!"},
			[]string{"Hello, world!"},
		},
		{
			"Add-mask", false,
			[]string{
				"foo mysecret bar",
				"::add-mask::mysecret",
				"foo mysecret bar",
			},
			[]string{
				"foo mysecret bar",
				"<nil>",
				"foo *** bar",
			},
		},
		{
			"Debug enabled", true,
			[]string{
				"::debug::GitHub Actions runtime token access controls",
			},
			[]string{
				"GitHub Actions runtime token access controls",
			},
		},
		{
			"Debug not enabled", false,
			[]string{
				"::debug::GitHub Actions runtime token access controls",
			},
			[]string{
				"<nil>",
			},
		},
		{
			"notice", false,
			[]string{
				"::notice file=file.name,line=42,endLine=48,title=Cool Title::Gosh, that's not going to work",
			},
			[]string{
				"::notice file=file.name,line=42,endLine=48,title=Cool Title::Gosh, that's not going to work",
			},
		},
		{
			"warning", false,
			[]string{
				"::warning file=file.name,line=42,endLine=48,title=Cool Title::Gosh, that's not going to work",
			},
			[]string{
				"::warning file=file.name,line=42,endLine=48,title=Cool Title::Gosh, that's not going to work",
			},
		},
		{
			"error", false,
			[]string{
				"::error file=file.name,line=42,endLine=48,title=Cool Title::Gosh, that's not going to work",
			},
			[]string{
				"::error file=file.name,line=42,endLine=48,title=Cool Title::Gosh, that's not going to work",
			},
		},
		{
			"group", false,
			[]string{
				"::group::",
				"::endgroup::",
			},
			[]string{
				"::group::",
				"::endgroup::",
			},
		},
		{
			"stop-commands", false,
			[]string{
				"::add-mask::foo",
				"::stop-commands::myverycoolstoptoken",
				"::add-mask::bar",
				"::debug::Stuff",
				"myverycoolstoptoken",
				"::add-mask::baz",
				"::myverycoolstoptoken::",
				"::add-mask::wibble",
				"foo bar baz wibble",
			},
			[]string{
				"<nil>",
				"<nil>",
				"::add-mask::bar",
				"::debug::Stuff",
				"myverycoolstoptoken",
				"::add-mask::baz",
				"<nil>",
				"<nil>",
				"*** bar baz ***",
			},
		},
		{
			"unknown command", false,
			[]string{
				"::set-mask::foo",
			},
			[]string{
				"::set-mask::foo",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reporter{
				logReplacer:        strings.NewReplacer(),
				debugOutputEnabled: tt.debugOutputEnabled,
			}
			for idx, arg := range tt.args {
				rv := r.parseLogRow(&log.Entry{Message: arg})
				got := "<nil>"

				if rv != nil {
					got = rv.Content
				}

				assert.Equal(t, tt.want[idx], got)
			}
		})
	}
}

func TestReporter_Fire(t *testing.T) {
	t.Run("ignore command lines", func(t *testing.T) {
		client := mocks.NewClient(t)
		client.On("UpdateLog", mock.Anything, mock.Anything).Return(func(_ context.Context, req *connect_go.Request[runnerv1.UpdateLogRequest]) (*connect_go.Response[runnerv1.UpdateLogResponse], error) {
			t.Logf("Received UpdateLog: %s", req.Msg.String())
			return connect_go.NewResponse(&runnerv1.UpdateLogResponse{
				AckIndex: req.Msg.Index + int64(len(req.Msg.Rows)),
			}), nil
		})
		client.On("UpdateTask", mock.Anything, mock.Anything).Return(func(_ context.Context, req *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
			t.Logf("Received UpdateTask: %s", req.Msg.String())
			return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		taskCtx, err := structpb.NewStruct(map[string]any{})
		require.NoError(t, err)
		cfg, _ := config.LoadDefault("")
		reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{
			Context: taskCtx,
		}, cfg)
		reporter.RunDaemon()
		defer func() {
			require.NoError(t, reporter.Close(""))
		}()
		reporter.ResetSteps(5)

		dataStep0 := map[string]any{
			"stage":      "Main",
			"stepNumber": 0,
			"raw_output": true,
		}

		require.NoError(t, reporter.Fire(&log.Entry{Message: "regular log line", Data: dataStep0}))
		require.NoError(t, reporter.Fire(&log.Entry{Message: "::debug::debug log line", Data: dataStep0}))
		require.NoError(t, reporter.Fire(&log.Entry{Message: "regular log line", Data: dataStep0}))
		require.NoError(t, reporter.Fire(&log.Entry{Message: "::debug::debug log line", Data: dataStep0}))
		require.NoError(t, reporter.Fire(&log.Entry{Message: "::debug::debug log line", Data: dataStep0}))
		require.NoError(t, reporter.Fire(&log.Entry{Message: "regular log line", Data: dataStep0}))
		require.NoError(t, reporter.Fire(&log.Entry{Message: "composite step result", Data: map[string]any{
			"stage":      "Main",
			"stepID":     []string{"0", "0"},
			"stepNumber": 0,
			"raw_output": true,
			"stepResult": "failure",
		}}))
		assert.Equal(t, runnerv1.Result_RESULT_UNSPECIFIED, reporter.state.Steps[0].Result)
		require.NoError(t, reporter.Fire(&log.Entry{Message: "step result", Data: map[string]any{
			"stage":      "Main",
			"stepNumber": 0,
			"raw_output": true,
			"stepResult": "success",
		}}))
		assert.Equal(t, runnerv1.Result_RESULT_SUCCESS, reporter.state.Steps[0].Result)

		assert.Equal(t, int64(5), reporter.state.Steps[0].LogLength)
	})
}

func TestReporter_LogLevelFiltering(t *testing.T) {
	// Set global level to Info so Debug entries should be filtered.
	origLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(origLevel)

	client := mocks.NewClient(t)
	client.On("UpdateLog", mock.Anything, mock.Anything).Return(func(_ context.Context, req *connect_go.Request[runnerv1.UpdateLogRequest]) (*connect_go.Response[runnerv1.UpdateLogResponse], error) {
		return connect_go.NewResponse(&runnerv1.UpdateLogResponse{
			AckIndex: req.Msg.Index + int64(len(req.Msg.Rows)),
		}), nil
	})
	client.On("UpdateTask", mock.Anything, mock.Anything).Return(func(_ context.Context, req *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
		return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	cfg, _ := config.LoadDefault("")
	reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{Context: taskCtx}, cfg)
	reporter.RunDaemon()
	defer func() {
		require.NoError(t, reporter.Close(""))
	}()
	reporter.ResetSteps(2)

	dataStep0 := log.Fields{"stage": "Main", "stepNumber": 0, "raw_output": true}
	dataStep0Internal := log.Fields{"stage": "Main", "stepNumber": 0}

	// raw_output entries always appear in job log regardless of level.
	require.NoError(t, reporter.Fire(&log.Entry{Message: "step output", Data: dataStep0, Level: log.InfoLevel}))
	require.NoError(t, reporter.Fire(&log.Entry{Message: "step debug output", Data: dataStep0, Level: log.DebugLevel}))
	assert.Equal(t, int64(2), reporter.state.Steps[0].LogLength, "raw_output entries must always be forwarded")

	// Non-raw_output entries during steps are not added to logRows regardless of level.
	require.NoError(t, reporter.Fire(&log.Entry{Message: "internal info", Data: dataStep0Internal, Level: log.InfoLevel}))
	require.NoError(t, reporter.Fire(&log.Entry{Message: "internal debug", Data: dataStep0Internal, Level: log.DebugLevel}))

	// stepResult at DebugLevel (skipped step) must still update state even when filtered from log.
	require.NoError(t, reporter.Fire(&log.Entry{
		Message: "Skipping step",
		Data: log.Fields{
			"stage":      "Main",
			"stepNumber": 1,
			"stepResult": "skipped",
		},
		Level: log.DebugLevel,
	}))
	assert.Equal(t, runnerv1.Result_RESULT_SKIPPED, reporter.state.Steps[1].Result,
		"stepResult at DebugLevel must update step state even when log entry is filtered from job log output")
}

// TestReporter_EphemeralRunnerDeletion reproduces the exact scenario from
// https://gitea.com/gitea/runner/issues/793:
//
//  1. RunDaemon calls ReportLog(false) — runner is still alive
//  2. Close() updates state to Result=FAILURE (between RunDaemon's ReportLog and ReportState)
//  3. RunDaemon's ReportState() would clone the completed state and send it,
//     but the fix makes ReportState return early when closed, preventing this
//  4. Close's ReportLog(true) succeeds because the runner was not deleted
func TestReporter_EphemeralRunnerDeletion(t *testing.T) {
	runnerDeleted := false

	client := mocks.NewClient(t)
	client.On("UpdateLog", mock.Anything, mock.Anything).Return(
		func(_ context.Context, req *connect_go.Request[runnerv1.UpdateLogRequest]) (*connect_go.Response[runnerv1.UpdateLogResponse], error) {
			if runnerDeleted {
				return nil, errors.New("runner has been deleted")
			}
			return connect_go.NewResponse(&runnerv1.UpdateLogResponse{
				AckIndex: req.Msg.Index + int64(len(req.Msg.Rows)),
			}), nil
		},
	)
	client.On("UpdateTask", mock.Anything, mock.Anything).Maybe().Return(
		func(_ context.Context, req *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
			// Server deletes ephemeral runner when it receives a completed state
			if req.Msg.State != nil && req.Msg.State.Result != runnerv1.Result_RESULT_UNSPECIFIED {
				runnerDeleted = true
			}
			return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	cfg, _ := config.LoadDefault("")
	reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{Context: taskCtx}, cfg)
	reporter.ResetSteps(1)

	// Fire a log entry to create pending data
	require.NoError(t, reporter.Fire(&log.Entry{
		Message: "build output",
		Data:    log.Fields{"stage": "Main", "stepNumber": 0, "raw_output": true},
	}))

	// Step 1: RunDaemon calls ReportLog(false) — runner is still alive
	require.NoError(t, reporter.ReportLog(false))

	// Step 2: Close() updates state — sets Result=FAILURE and marks steps cancelled.
	// In the real race, this happens while RunDaemon is between ReportLog and ReportState.
	reporter.stateMu.Lock()
	reporter.closed = true
	for _, v := range reporter.state.Steps {
		if v.Result == runnerv1.Result_RESULT_UNSPECIFIED {
			v.Result = runnerv1.Result_RESULT_CANCELLED
		}
	}
	reporter.state.Result = runnerv1.Result_RESULT_FAILURE
	reporter.logRows = append(reporter.logRows, &runnerv1.LogRow{
		Time:    timestamppb.Now(),
		Content: "Early termination",
	})
	reporter.state.StoppedAt = timestamppb.Now()
	reporter.stateMu.Unlock()

	// Step 3: RunDaemon's ReportState() — with the fix, this returns early
	// because closed=true, preventing the server from deleting the runner.
	require.NoError(t, reporter.ReportState(false))
	assert.False(t, runnerDeleted, "runner must not be deleted by RunDaemon's ReportState")

	// Step 4: Close's final log upload succeeds because the runner is still alive.
	// Flush pending rows first, then send the noMore signal (matching Close's retry behavior).
	require.NoError(t, reporter.ReportLog(false))
	// Acknowledge Close as done in daemon
	close(reporter.daemon)
	err = reporter.ReportLog(true)
	require.NoError(t, err, "final log upload must not fail: runner should not be deleted before Close finishes sending logs")
	err = reporter.ReportState(true)
	require.NoError(t, err, "final state update should work: runner should not be deleted before Close finishes sending logs")
}

func TestReporter_RunDaemonClose_Race(t *testing.T) {
	client := mocks.NewClient(t)
	client.On("UpdateLog", mock.Anything, mock.Anything).Return(
		func(_ context.Context, req *connect_go.Request[runnerv1.UpdateLogRequest]) (*connect_go.Response[runnerv1.UpdateLogResponse], error) {
			return connect_go.NewResponse(&runnerv1.UpdateLogResponse{
				AckIndex: req.Msg.Index + int64(len(req.Msg.Rows)),
			}), nil
		},
	)
	client.On("UpdateTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, req *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
			return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	cfg, _ := config.LoadDefault("")
	reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{
		Context: taskCtx,
	}, cfg)
	reporter.ResetSteps(1)

	// Start the daemon loop — RunDaemon spawns a goroutine internally.
	reporter.RunDaemon()

	// Close concurrently — this races with the daemon goroutine on r.closed.
	require.NoError(t, reporter.Close(""))

	// Cancel context so the daemon goroutine exits cleanly.
	cancel()
}

// TestReporter_MaxLatencyTimer verifies that the maxLatencyTimer flushes a
// single buffered log row before the periodic logTicker fires.
//
// Setup: logReportInterval=10s (effectively never), maxLatency=100ms.
// Fire one log line, then assert UpdateLog is called within 500ms.
func TestReporter_MaxLatencyTimer(t *testing.T) {
	var updateLogCalls atomic.Int64

	client := mocks.NewClient(t)
	client.On("UpdateLog", mock.Anything, mock.Anything).Return(
		func(_ context.Context, req *connect_go.Request[runnerv1.UpdateLogRequest]) (*connect_go.Response[runnerv1.UpdateLogResponse], error) {
			updateLogCalls.Add(1)
			return connect_go.NewResponse(&runnerv1.UpdateLogResponse{
				AckIndex: req.Msg.Index + int64(len(req.Msg.Rows)),
			}), nil
		},
	)
	client.On("UpdateTask", mock.Anything, mock.Anything).Maybe().Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
			return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)

	// Custom config: logTicker=10s (won't fire during test), maxLatency=100ms
	cfg, _ := config.LoadDefault("")
	cfg.Runner.LogReportInterval = 10 * time.Second
	cfg.Runner.LogReportMaxLatency = 100 * time.Millisecond
	cfg.Runner.LogReportBatchSize = 1000 // won't trigger batch flush

	reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{Context: taskCtx}, cfg)
	reporter.ResetSteps(1)
	reporter.RunDaemon()
	defer func() {
		_ = reporter.Close("")
	}()

	// Fire a single log line — not enough to trigger batch flush
	require.NoError(t, reporter.Fire(&log.Entry{
		Message: "single log line",
		Data:    log.Fields{"stage": "Main", "stepNumber": 0, "raw_output": true},
	}))

	// maxLatencyTimer should flush within ~100ms. Wait up to 500ms.
	assert.Eventually(t, func() bool {
		return updateLogCalls.Load() > 0
	}, 500*time.Millisecond, 10*time.Millisecond,
		"maxLatencyTimer should have flushed the log before logTicker (10s)")
}

// TestReporter_BatchSizeFlush verifies that reaching logBatchSize triggers
// an immediate log flush without waiting for any timer.
func TestReporter_BatchSizeFlush(t *testing.T) {
	var updateLogCalls atomic.Int64

	client := mocks.NewClient(t)
	client.On("UpdateLog", mock.Anything, mock.Anything).Return(
		func(_ context.Context, req *connect_go.Request[runnerv1.UpdateLogRequest]) (*connect_go.Response[runnerv1.UpdateLogResponse], error) {
			updateLogCalls.Add(1)
			return connect_go.NewResponse(&runnerv1.UpdateLogResponse{
				AckIndex: req.Msg.Index + int64(len(req.Msg.Rows)),
			}), nil
		},
	)
	client.On("UpdateTask", mock.Anything, mock.Anything).Maybe().Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
			return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)

	// Custom config: large timers, small batch size
	cfg, _ := config.LoadDefault("")
	cfg.Runner.LogReportInterval = 10 * time.Second
	cfg.Runner.LogReportMaxLatency = 10 * time.Second
	cfg.Runner.LogReportBatchSize = 5

	reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{Context: taskCtx}, cfg)
	reporter.ResetSteps(1)
	reporter.RunDaemon()
	defer func() {
		_ = reporter.Close("")
	}()

	// Fire exactly batchSize log lines
	for i := range 5 {
		require.NoError(t, reporter.Fire(&log.Entry{
			Message: fmt.Sprintf("log line %d", i),
			Data:    log.Fields{"stage": "Main", "stepNumber": 0, "raw_output": true},
		}))
	}

	// Batch threshold should trigger immediate flush
	assert.Eventually(t, func() bool {
		return updateLogCalls.Load() > 0
	}, 500*time.Millisecond, 10*time.Millisecond,
		"batch size threshold should have triggered immediate flush")
}

// TestReporter_StateChangedNotLostDuringReport asserts that a Fire() arriving
// mid-UpdateTask re-dirties the flag so the change is picked up by the next report.
func TestReporter_StateChangedNotLostDuringReport(t *testing.T) {
	var updateTaskCalls atomic.Int64
	inFlight := make(chan struct{})
	release := make(chan struct{})

	client := mocks.NewClient(t)
	client.On("UpdateTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
			n := updateTaskCalls.Add(1)
			if n == 1 {
				// Signal that the first UpdateTask is in flight, then block until released.
				close(inFlight)
				<-release
			}
			return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	cfg, _ := config.LoadDefault("")
	reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{Context: taskCtx}, cfg)
	reporter.ResetSteps(2)

	// Mark stateChanged=true so the first ReportState proceeds to UpdateTask.
	reporter.stateMu.Lock()
	reporter.stateChanged = true
	reporter.stateMu.Unlock()

	// Kick off the first ReportState in a goroutine — it will block in UpdateTask.
	done := make(chan error, 1)
	go func() {
		done <- reporter.ReportState(false)
	}()

	// Wait until UpdateTask is in flight (snapshot taken, flag consumed).
	<-inFlight

	// Concurrent Fire() modifies state — must re-flip stateChanged so the
	// change is not lost when the in-flight ReportState finishes.
	require.NoError(t, reporter.Fire(&log.Entry{
		Message: "step starts",
		Data:    log.Fields{"stage": "Main", "stepNumber": 1, "raw_output": true},
	}))

	// Release the in-flight UpdateTask and wait for it to return.
	close(release)
	require.NoError(t, <-done)

	// stateChanged must still be true so the next ReportState picks up the
	// concurrent Fire()'s change instead of skipping via the early-return path.
	reporter.stateMu.RLock()
	changed := reporter.stateChanged
	reporter.stateMu.RUnlock()
	assert.True(t, changed, "stateChanged must remain true after a concurrent Fire() during in-flight ReportState")

	// And the next ReportState must actually send a second UpdateTask.
	require.NoError(t, reporter.ReportState(false))
	assert.Equal(t, int64(2), updateTaskCalls.Load(), "concurrent Fire() change must trigger a second UpdateTask, not be silently lost")
}

// TestReporter_StateChangedRestoredOnError verifies that when UpdateTask fails,
// the dirty flag is restored so the snapshotted change isn't silently lost.
func TestReporter_StateChangedRestoredOnError(t *testing.T) {
	var updateTaskCalls atomic.Int64

	client := mocks.NewClient(t)
	client.On("UpdateTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
			n := updateTaskCalls.Add(1)
			if n == 1 {
				return nil, errors.New("transient network error")
			}
			return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	cfg, _ := config.LoadDefault("")
	reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{Context: taskCtx}, cfg)
	reporter.ResetSteps(1)

	reporter.stateMu.Lock()
	reporter.stateChanged = true
	reporter.stateMu.Unlock()

	// First ReportState fails — flag must be restored to true.
	require.Error(t, reporter.ReportState(false))

	reporter.stateMu.RLock()
	changed := reporter.stateChanged
	reporter.stateMu.RUnlock()
	assert.True(t, changed, "stateChanged must be restored to true after UpdateTask error so the change is retried")

	// The next ReportState should still issue a request because the flag was restored.
	require.NoError(t, reporter.ReportState(false))
	assert.Equal(t, int64(2), updateTaskCalls.Load())
}

// TestReporter_StateNotifyFlush verifies that step transitions trigger
// an immediate state flush via the stateNotify channel.
func TestReporter_StateNotifyFlush(t *testing.T) {
	var updateTaskCalls atomic.Int64

	client := mocks.NewClient(t)
	client.On("UpdateLog", mock.Anything, mock.Anything).Maybe().Return(
		func(_ context.Context, req *connect_go.Request[runnerv1.UpdateLogRequest]) (*connect_go.Response[runnerv1.UpdateLogResponse], error) {
			return connect_go.NewResponse(&runnerv1.UpdateLogResponse{
				AckIndex: req.Msg.Index + int64(len(req.Msg.Rows)),
			}), nil
		},
	)
	client.On("UpdateTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
			updateTaskCalls.Add(1)
			return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)

	// Custom config: large state interval so only stateNotify can trigger
	cfg, _ := config.LoadDefault("")
	cfg.Runner.StateReportInterval = 10 * time.Second
	cfg.Runner.LogReportInterval = 10 * time.Second

	reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{Context: taskCtx}, cfg)
	reporter.ResetSteps(1)
	reporter.RunDaemon()
	defer func() {
		_ = reporter.Close("")
	}()

	// Fire a log entry that starts a step — this triggers stateNotify
	require.NoError(t, reporter.Fire(&log.Entry{
		Message: "step starting",
		Data:    log.Fields{"stage": "Main", "stepNumber": 0, "raw_output": true},
	}))

	// stateNotify should trigger immediate UpdateTask call
	assert.Eventually(t, func() bool {
		return updateTaskCalls.Load() > 0
	}, 500*time.Millisecond, 10*time.Millisecond,
		"step transition should have triggered immediate state flush via stateNotify")
}

// Regression test for https://gitea.com/gitea/runner/issues/950: Close() must
// always send a final UpdateLog with NoMore=true carrying at least one row,
// otherwise the server's len(Rows)==0 short-circuit skips TransferLogs.
// TODO: Remove after https://github.com/go-gitea/gitea/pull/37631 is in all
// supported branches, e.g. v1.28+.
func TestReporter_CloseAlwaysSendsRowsWithNoMore(t *testing.T) {
	var lastReq atomic.Pointer[runnerv1.UpdateLogRequest]
	var noMoreCalls atomic.Int64

	client := mocks.NewClient(t)
	client.On("UpdateLog", mock.Anything, mock.Anything).Return(
		func(_ context.Context, req *connect_go.Request[runnerv1.UpdateLogRequest]) (*connect_go.Response[runnerv1.UpdateLogResponse], error) {
			lastReq.Store(req.Msg)
			if req.Msg.NoMore {
				noMoreCalls.Add(1)
			}
			return connect_go.NewResponse(&runnerv1.UpdateLogResponse{
				AckIndex: req.Msg.Index + int64(len(req.Msg.Rows)),
			}), nil
		},
	)
	client.On("UpdateTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
			return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)

	// Intervals large enough that no daemon-driven flush fires during the test.
	cfg, _ := config.LoadDefault("")
	cfg.Runner.LogReportInterval = 10 * time.Second
	cfg.Runner.LogReportMaxLatency = 10 * time.Second
	cfg.Runner.StateReportInterval = 10 * time.Second
	cfg.Runner.LogReportBatchSize = 1000

	reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{Context: taskCtx}, cfg)
	reporter.ResetSteps(1)
	reporter.RunDaemon()

	// Simulate a successful job whose log buffer was already drained by the
	// daemon (logOffset > 0, logRows empty, terminal Result set). This is the
	// state Close() lands in for the typical successful job under #819.
	reporter.stateMu.Lock()
	reporter.logOffset = 5
	reporter.state.Result = runnerv1.Result_RESULT_SUCCESS
	reporter.state.Steps[0].Result = runnerv1.Result_RESULT_SUCCESS
	reporter.state.StoppedAt = timestamppb.Now()
	reporter.stateMu.Unlock()

	require.NoError(t, reporter.Close(""))

	require.Equal(t, int64(1), noMoreCalls.Load(), "Close must send exactly one UpdateLog with NoMore=true")
	final := lastReq.Load()
	require.NotNil(t, final)
	assert.True(t, final.NoMore, "final UpdateLog must carry NoMore=true")
	assert.NotEmpty(t, final.Rows, "final UpdateLog must carry at least one row")
}

// TestReporter_StateHeartbeat verifies that ReportState sends a heartbeat
// UpdateTask once stateReportInterval has elapsed since the last successful
// report, even when nothing has changed. Without this, long-running silent
// jobs (no log output, no step transitions) cause the server to time the
// task out and cancel it (#826).
func TestReporter_StateHeartbeat(t *testing.T) {
	var updateTaskCalls atomic.Int64

	client := mocks.NewClient(t)
	client.On("UpdateTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
			updateTaskCalls.Add(1)
			return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	cfg, _ := config.LoadDefault("")
	cfg.Runner.StateReportInterval = 50 * time.Millisecond
	reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{Context: taskCtx}, cfg)
	reporter.ResetSteps(1)

	// First call has no prior report — sends to seed lastReportedAt.
	reporter.stateMu.Lock()
	reporter.stateChanged = true
	reporter.stateMu.Unlock()
	require.NoError(t, reporter.ReportState(false))
	require.Equal(t, int64(1), updateTaskCalls.Load())

	// Second call immediately after with nothing changed — must skip.
	require.NoError(t, reporter.ReportState(false))
	assert.Equal(t, int64(1), updateTaskCalls.Load(), "no-op ReportState within stateReportInterval must skip")

	// After stateReportInterval elapses, a heartbeat must fire even with no changes.
	time.Sleep(2 * cfg.Runner.StateReportInterval)
	require.NoError(t, reporter.ReportState(false))
	assert.Equal(t, int64(2), updateTaskCalls.Load(), "ReportState must heartbeat after stateReportInterval even with no state change")
}

// TestReporter_ServerCancelStillFlushesFinal asserts that when the Gitea server
// returns RESULT_CANCELLED on an in-flight UpdateTask (which causes the
// reporter to cancel the task context), Close() still successfully sends the
// final UpdateLog{NoMore:true} and the final UpdateTask carrying the populated
// final state. Before the fix this final flush used r.ctx, which was just
// cancelled, so retry-go aborted on its context check and Gitea never received
// the runner's acknowledgement of the cancel.
func TestReporter_ServerCancelStillFlushesFinal(t *testing.T) {
	var (
		updateTaskCalls    atomic.Int64
		finalLogNoMoreSeen atomic.Bool
		finalTaskStateSeen atomic.Bool
	)

	client := mocks.NewClient(t)
	client.On("UpdateLog", mock.Anything, mock.Anything).Return(
		func(_ context.Context, req *connect_go.Request[runnerv1.UpdateLogRequest]) (*connect_go.Response[runnerv1.UpdateLogResponse], error) {
			if req.Msg.NoMore {
				finalLogNoMoreSeen.Store(true)
			}
			return connect_go.NewResponse(&runnerv1.UpdateLogResponse{
				AckIndex: req.Msg.Index + int64(len(req.Msg.Rows)),
			}), nil
		},
	)
	// The first UpdateTask returns RESULT_CANCELLED — modelling a server-side
	// cancellation; the reporter must call r.cancel() in response. The final
	// UpdateTask issued by Close() must still arrive even though r.ctx is now
	// cancelled.
	client.On("UpdateTask", mock.Anything, mock.Anything).Return(
		func(_ context.Context, req *connect_go.Request[runnerv1.UpdateTaskRequest]) (*connect_go.Response[runnerv1.UpdateTaskResponse], error) {
			n := updateTaskCalls.Add(1)
			if n == 1 {
				return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{
					State: &runnerv1.TaskState{
						Result: runnerv1.Result_RESULT_CANCELLED,
					},
				}), nil
			}
			if req.Msg.State != nil && req.Msg.State.Result != runnerv1.Result_RESULT_UNSPECIFIED {
				finalTaskStateSeen.Store(true)
			}
			return connect_go.NewResponse(&runnerv1.UpdateTaskResponse{}), nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	cfg, _ := config.LoadDefault("")
	reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{Context: taskCtx}, cfg)
	reporter.ResetSteps(1)

	// Force the first ReportState to actually call UpdateTask.
	reporter.stateMu.Lock()
	reporter.stateChanged = true
	reporter.stateMu.Unlock()

	// First ReportState — server returns RESULT_CANCELLED, reporter cancels r.ctx.
	require.NoError(t, reporter.ReportState(false))
	require.Equal(t, int64(1), updateTaskCalls.Load())

	select {
	case <-ctx.Done():
		// Expected: reporter called cancel() because the server reported the task as cancelled.
	case <-time.After(time.Second):
		t.Fatal("expected r.ctx to be cancelled after server returned RESULT_CANCELLED")
	}

	// The test does not start the daemon goroutine; close(r.daemon) so Close()
	// proceeds without waiting on its 60s timeout.
	close(reporter.daemon)

	// Now Close() runs. Before the fix, both final RPCs aborted on the cancelled
	// r.ctx via retry.Context. After the fix, Close() uses a detached context and
	// the per-RPC rpcCtx() falls back to a fresh ctx, so both calls succeed.
	require.NoError(t, reporter.Close("cancelled"))

	assert.True(t, finalLogNoMoreSeen.Load(), "Close() must send a final UpdateLog{NoMore:true} even after server-side cancellation")
	assert.True(t, finalTaskStateSeen.Load(), "Close() must send a final UpdateTask with the populated final state even after server-side cancellation")
}
