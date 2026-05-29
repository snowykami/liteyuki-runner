// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package report

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitea.com/gitea/runner/internal/pkg/client"
	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/metrics"

	runnerv1 "code.gitea.io/actions-proto-go/runner/v1"
	"connectrpc.com/connect"
	"github.com/avast/retry-go/v5"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Reporter struct {
	ctx    context.Context
	cancel context.CancelFunc

	closed  bool
	client  client.Client
	clientM sync.Mutex

	logOffset   int
	logRows     []*runnerv1.LogRow
	logReplacer *strings.Replacer
	oldnew      []string

	// lastLogBufferRows is the last value written to the ReportLogBufferRows
	// gauge; guarded by clientM (the same lock held around each ReportLog call)
	// so the gauge skips no-op Set calls when the buffer size is unchanged.
	lastLogBufferRows int

	state        *runnerv1.TaskState
	stateChanged bool
	stateMu      sync.RWMutex
	outputs      sync.Map
	daemon       chan struct{}

	// Unix-nanos of the last successful UpdateTask. Atomic so the heartbeat
	// guard in ReportState reads it without contending stateMu.
	lastReportedAtNanos atomic.Int64

	// Adaptive batching control
	logReportInterval   time.Duration
	logReportMaxLatency time.Duration
	logBatchSize        int
	stateReportInterval time.Duration
	// closeTimeout bounds each RPC attempt in the final flush, on a context
	// detached from r.ctx so a server cancel can't abort the acknowledgement.
	closeTimeout time.Duration

	// Event notification channels (non-blocking, buffered 1)
	logNotify   chan struct{} // signal: new log rows arrived
	stateNotify chan struct{} // signal: step transition (start/stop)

	debugOutputEnabled  bool
	stopCommandEndToken string
}

func NewReporter(ctx context.Context, cancel context.CancelFunc, client client.Client, task *runnerv1.Task, cfg *config.Config) *Reporter {
	var oldnew []string
	if v := task.Context.Fields["token"].GetStringValue(); v != "" {
		oldnew = append(oldnew, v, "***")
	}
	if v := task.Context.Fields["gitea_runtime_token"].GetStringValue(); v != "" {
		oldnew = append(oldnew, v, "***")
	}
	for _, v := range task.Secrets {
		oldnew = append(oldnew, v, "***")
	}

	rv := &Reporter{
		ctx:                 ctx,
		cancel:              cancel,
		client:              client,
		oldnew:              oldnew,
		logReplacer:         strings.NewReplacer(oldnew...),
		logReportInterval:   cfg.Runner.LogReportInterval,
		logReportMaxLatency: cfg.Runner.LogReportMaxLatency,
		logBatchSize:        cfg.Runner.LogReportBatchSize,
		stateReportInterval: cfg.Runner.StateReportInterval,
		closeTimeout:        cfg.Runner.ReportCloseTimeout,
		logNotify:           make(chan struct{}, 1),
		stateNotify:         make(chan struct{}, 1),
		state: &runnerv1.TaskState{
			Id: task.Id,
		},
		daemon: make(chan struct{}),
	}

	if task.Secrets["ACTIONS_STEP_DEBUG"] == "true" {
		rv.debugOutputEnabled = true
	}

	return rv
}

// Result returns the final job result. Safe to call after Close() returns.
func (r *Reporter) Result() runnerv1.Result {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.state.Result
}

func (r *Reporter) ResetSteps(l int) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	for i := range l {
		r.state.Steps = append(r.state.Steps, &runnerv1.StepState{
			Id: int64(i),
		})
	}
}

func (r *Reporter) Levels() []log.Level {
	return log.AllLevels
}

func appendIfNotNil[T any](s []*T, v *T) []*T {
	if v != nil {
		return append(s, v)
	}
	return s
}

// isJobStepEntry is used to not report composite step results incorrectly as step result
// returns true if the logentry is on job level
// returns false for composite action step messages
func isJobStepEntry(entry *log.Entry) bool {
	if v, ok := entry.Data["stepID"]; ok {
		if v, ok := v.([]string); ok && len(v) > 1 {
			return false
		}
	}
	return true
}

// notifyLog sends a non-blocking signal that new log rows are available.
func (r *Reporter) notifyLog() {
	select {
	case r.logNotify <- struct{}{}:
	default:
	}
}

// notifyState sends a non-blocking signal that a UX-critical state change occurred (step start/stop, job result).
func (r *Reporter) notifyState() {
	select {
	case r.stateNotify <- struct{}{}:
	default:
	}
}

// unlockAndNotify releases stateMu and sends channel notifications.
// Must be called with stateMu held.
func (r *Reporter) unlockAndNotify(urgentState bool) {
	r.stateMu.Unlock()
	r.notifyLog()
	if urgentState {
		r.notifyState()
	}
}

func (r *Reporter) Fire(entry *log.Entry) error {
	urgentState := false

	r.stateMu.Lock()

	r.stateChanged = true

	if log.IsLevelEnabled(log.TraceLevel) {
		log.WithFields(entry.Data).Trace(entry.Message)
	}

	timestamp := entry.Time
	if r.state.StartedAt == nil {
		r.state.StartedAt = timestamppb.New(timestamp)
	}

	stage := entry.Data["stage"]

	if stage != "Main" {
		if v, ok := entry.Data["jobResult"]; ok {
			if jobResult, ok := r.parseResult(v); ok {
				// We need to ensure log upload before this upload
				r.state.Result = jobResult
				r.state.StoppedAt = timestamppb.New(timestamp)
				for _, s := range r.state.Steps {
					if s.Result == runnerv1.Result_RESULT_UNSPECIFIED {
						s.Result = runnerv1.Result_RESULT_CANCELLED
						if jobResult == runnerv1.Result_RESULT_SKIPPED {
							s.Result = runnerv1.Result_RESULT_SKIPPED
						}
					}
				}
				urgentState = true
			}
		}
		if r.shouldAppendLogRow(entry) {
			r.logRows = appendIfNotNil(r.logRows, r.parseLogRow(entry))
		}
		r.unlockAndNotify(urgentState)
		return nil
	}

	var step *runnerv1.StepState
	if v, ok := entry.Data["stepNumber"]; ok {
		if v, ok := v.(int); ok && len(r.state.Steps) > v {
			step = r.state.Steps[v]
		}
	}
	if step == nil {
		if r.shouldAppendLogRow(entry) {
			r.logRows = appendIfNotNil(r.logRows, r.parseLogRow(entry))
		}
		r.unlockAndNotify(false)
		return nil
	}

	if step.StartedAt == nil {
		step.StartedAt = timestamppb.New(timestamp)
		urgentState = true
	}

	// Force reporting log errors as raw output to prevent silent failures
	if entry.Level == log.ErrorLevel {
		entry.Data["raw_output"] = true
	}

	if v, ok := entry.Data["raw_output"]; ok {
		if rawOutput, ok := v.(bool); ok && rawOutput {
			if row := r.parseLogRow(entry); row != nil {
				if step.LogLength == 0 {
					step.LogIndex = int64(r.logOffset + len(r.logRows))
				}
				step.LogLength++
				r.logRows = append(r.logRows, row)
			}
		}
	} else if r.shouldAppendLogRow(entry) {
		r.logRows = appendIfNotNil(r.logRows, r.parseLogRow(entry))
	}
	if v, ok := entry.Data["stepResult"]; ok && isJobStepEntry(entry) {
		if stepResult, ok := r.parseResult(v); ok {
			if step.LogLength == 0 {
				step.LogIndex = int64(r.logOffset + len(r.logRows))
			}
			step.Result = stepResult
			step.StoppedAt = timestamppb.New(timestamp)
			urgentState = true
		}
	}

	r.unlockAndNotify(urgentState)
	return nil
}

func (r *Reporter) RunDaemon() {
	go r.runDaemonLoop()
}

func (r *Reporter) stopLatencyTimer(active *bool, timer *time.Timer) {
	if *active {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		*active = false
	}
}

func (r *Reporter) runDaemonLoop() {
	logTicker := time.NewTicker(r.logReportInterval)
	stateTicker := time.NewTicker(r.stateReportInterval)

	// maxLatencyTimer ensures the first buffered log row is sent within logReportMaxLatency.
	// Start inactive — it is armed when the first log row arrives in an empty buffer.
	maxLatencyTimer := time.NewTimer(0)
	if !maxLatencyTimer.Stop() {
		<-maxLatencyTimer.C
	}
	maxLatencyActive := false

	defer logTicker.Stop()
	defer stateTicker.Stop()
	defer maxLatencyTimer.Stop()

	for {
		select {
		case <-logTicker.C:
			_ = r.ReportLog(false)
			r.stopLatencyTimer(&maxLatencyActive, maxLatencyTimer)

		case <-stateTicker.C:
			_ = r.ReportState(false)

		case <-r.logNotify:
			r.stateMu.RLock()
			n := len(r.logRows)
			r.stateMu.RUnlock()

			if n >= r.logBatchSize {
				_ = r.ReportLog(false)
				r.stopLatencyTimer(&maxLatencyActive, maxLatencyTimer)
			} else if !maxLatencyActive && n > 0 {
				maxLatencyTimer.Reset(r.logReportMaxLatency)
				maxLatencyActive = true
			}

		case <-r.stateNotify:
			// Step transition or job result — flush both immediately for frontend UX.
			_ = r.ReportLog(false)
			_ = r.ReportState(false)
			r.stopLatencyTimer(&maxLatencyActive, maxLatencyTimer)

		case <-maxLatencyTimer.C:
			maxLatencyActive = false
			_ = r.ReportLog(false)

		case <-r.ctx.Done():
			// Stop heartbeating on cancel so Gitea sees the runner as offline
			// during cleanup and won't assign an overlapping task. Close() still
			// delivers the final flush on a detached context (flushFinal).
			close(r.daemon)
			return
		}

		r.stateMu.RLock()
		closed := r.closed
		r.stateMu.RUnlock()
		if closed {
			close(r.daemon)
			return
		}
	}
}

func (r *Reporter) Logf(format string, a ...any) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	r.logf(format, a...)
}

func (r *Reporter) logf(format string, a ...any) {
	if !r.duringSteps() {
		r.logRows = append(r.logRows, &runnerv1.LogRow{
			Time:    timestamppb.Now(),
			Content: fmt.Sprintf(format, a...),
		})
	}
}

func (r *Reporter) SetOutputs(outputs map[string]string) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	for k, v := range outputs {
		if len(k) > 255 {
			r.logf("ignore output because the key is too long: %q", k)
			continue
		}
		if l := len(v); l > 1024*1024 {
			log.Println("ignore output because the value is too long:", k, l)
			r.logf("ignore output because the value %q is too long: %d", k, l)
		}
		if _, ok := r.outputs.Load(k); ok {
			continue
		}
		r.outputs.Store(k, v)
	}
}

func (r *Reporter) Close(lastWords string) error {
	r.stateMu.Lock()
	r.closed = true
	if r.state.Result == runnerv1.Result_RESULT_UNSPECIFIED {
		if lastWords == "" {
			lastWords = "Early termination"
		}
		for _, v := range r.state.Steps {
			if v.Result == runnerv1.Result_RESULT_UNSPECIFIED {
				v.Result = runnerv1.Result_RESULT_CANCELLED
			}
		}
		r.state.Result = runnerv1.Result_RESULT_FAILURE
		r.logRows = append(r.logRows, &runnerv1.LogRow{
			Time:    timestamppb.Now(),
			Content: lastWords,
		})
		r.state.StoppedAt = timestamppb.Now()
	} else if lastWords != "" {
		r.logRows = append(r.logRows, &runnerv1.LogRow{
			Time:    timestamppb.Now(),
			Content: lastWords,
		})
	}
	r.stateMu.Unlock()

	// Wake up the daemon loop so it detects closed promptly.
	r.notifyLog()

	// Wait for Acknowledge
	select {
	case <-r.daemon:
	case <-time.After(60 * time.Second):
		close(r.daemon)
		log.Error("No Response from RunDaemon for 60s, continue best effort")
	}

	// Gitea's UpdateLog short-circuits on len(Rows)==0 before honoring NoMore,
	// so a final empty request never runs TransferLogs and dbfs_data leaks.
	// Inject a sentinel row after the daemon has exited so it can't be flushed
	// before ReportLog(true).
	// TODO: Remove after https://github.com/go-gitea/gitea/pull/37631 is in all
	// supported branches, e.g. v1.28+.
	r.stateMu.Lock()
	if len(r.logRows) == 0 {
		r.logRows = append(r.logRows, &runnerv1.LogRow{
			Time:    timestamppb.Now(),
			Content: "",
		})
	}
	r.stateMu.Unlock()

	// Separate budgets so a slow ReportLog can't starve the ReportState that
	// carries the cancel acknowledgement.
	return errors.Join(
		r.flushFinal(func() error { return r.ReportLog(true) }),
		r.flushFinal(func() error { return r.ReportState(true) }),
	)
}

// flushFinal retries fn on a detached, bounded context so a cancelled r.ctx
// does not abort the final flush. Each call gets its own fresh budget.
func (r *Reporter) flushFinal(fn func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*r.effectiveCloseTimeout())
	defer cancel()
	return retry.New(retry.Context(ctx)).Do(fn)
}

// effectiveCloseTimeout returns closeTimeout, or 10s when unset, so a zero
// value can't produce an already-expired context for the final flush.
func (r *Reporter) effectiveCloseTimeout() time.Duration {
	if r.closeTimeout <= 0 {
		return 10 * time.Second
	}
	return r.closeTimeout
}

// rpcCtx returns the context for an outbound RPC plus a cancel func. While
// r.ctx is alive it's used directly; once cancelled (server RESULT_CANCELLED),
// RPCs switch to a fresh bounded context so Close()'s final flush still lands.
func (r *Reporter) rpcCtx() (context.Context, context.CancelFunc) {
	select {
	case <-r.ctx.Done():
		return context.WithTimeout(context.Background(), r.effectiveCloseTimeout())
	default:
		return r.ctx, func() {}
	}
}

func (r *Reporter) ReportLog(noMore bool) error {
	r.clientM.Lock()
	defer r.clientM.Unlock()

	r.stateMu.RLock()
	rows := r.logRows
	r.stateMu.RUnlock()

	if !noMore && len(rows) == 0 {
		return nil
	}

	rpcCtx, rpcCancel := r.rpcCtx()
	defer rpcCancel()

	start := time.Now()
	resp, err := r.client.UpdateLog(rpcCtx, connect.NewRequest(&runnerv1.UpdateLogRequest{
		TaskId: r.state.Id,
		Index:  int64(r.logOffset),
		Rows:   rows,
		NoMore: noMore,
	}))
	metrics.ReportLogDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ReportLogTotal.WithLabelValues(metrics.LabelResultError).Inc()
		metrics.ClientErrors.WithLabelValues(metrics.LabelMethodUpdateLog).Inc()
		return err
	}
	metrics.ReportLogTotal.WithLabelValues(metrics.LabelResultSuccess).Inc()

	ack := int(resp.Msg.AckIndex)
	if ack < r.logOffset {
		return errors.New("submitted logs are lost")
	}

	r.stateMu.Lock()
	r.logRows = r.logRows[ack-r.logOffset:]
	submitted := r.logOffset + len(rows)
	r.logOffset = ack
	remaining := len(r.logRows)
	r.stateMu.Unlock()
	if remaining != r.lastLogBufferRows {
		metrics.ReportLogBufferRows.Set(float64(remaining))
		r.lastLogBufferRows = remaining
	}

	if noMore && ack < submitted {
		return errors.New("not all logs are submitted")
	}

	return nil
}

// ReportState only reports the job result if reportResult is true
// RunDaemon never reports results even if result is set
func (r *Reporter) ReportState(reportResult bool) error {
	r.clientM.Lock()
	defer r.clientM.Unlock()

	// Build the outputs map first (single Range pass instead of two).
	outputs := make(map[string]string)
	r.outputs.Range(func(k, v any) bool {
		if val, ok := v.(string); ok {
			outputs[k.(string)] = val
		}
		return true
	})

	// Consume stateChanged atomically with the snapshot; restored on error
	// below so a concurrent Fire() during UpdateTask isn't silently lost.
	// Heartbeat at stateReportInterval even when nothing changed, so the server
	// doesn't time out long-running silent jobs as orphaned (#826).
	last := r.lastReportedAtNanos.Load()
	withinHeartbeatInterval := last != 0 && time.Since(time.Unix(0, last)) < r.stateReportInterval
	r.stateMu.Lock()
	if !reportResult && !r.stateChanged && len(outputs) == 0 && withinHeartbeatInterval {
		r.stateMu.Unlock()
		return nil
	}
	state := proto.Clone(r.state).(*runnerv1.TaskState)
	r.stateChanged = false
	r.stateMu.Unlock()

	if !reportResult {
		state.Result = runnerv1.Result_RESULT_UNSPECIFIED
	}

	rpcCtx, rpcCancel := r.rpcCtx()
	defer rpcCancel()

	start := time.Now()
	resp, err := r.client.UpdateTask(rpcCtx, connect.NewRequest(&runnerv1.UpdateTaskRequest{
		State:   state,
		Outputs: outputs,
	}))
	metrics.ReportStateDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ReportStateTotal.WithLabelValues(metrics.LabelResultError).Inc()
		metrics.ClientErrors.WithLabelValues(metrics.LabelMethodUpdateTask).Inc()
		r.stateMu.Lock()
		r.stateChanged = true
		r.stateMu.Unlock()
		return err
	}
	metrics.ReportStateTotal.WithLabelValues(metrics.LabelResultSuccess).Inc()
	r.lastReportedAtNanos.Store(time.Now().UnixNano())

	for _, k := range resp.Msg.SentOutputs {
		r.outputs.Store(k, struct{}{})
	}

	if resp.Msg.State != nil && resp.Msg.State.Result == runnerv1.Result_RESULT_CANCELLED {
		r.cancel()
	}

	var noSent []string
	r.outputs.Range(func(k, v any) bool {
		if _, ok := v.(string); ok {
			noSent = append(noSent, k.(string))
		}
		return true
	})
	if len(noSent) > 0 {
		return fmt.Errorf("there are still outputs that have not been sent: %v", noSent)
	}

	return nil
}

func (r *Reporter) duringSteps() bool {
	if steps := r.state.Steps; len(steps) == 0 {
		return false
	} else if first := steps[0]; first.Result == runnerv1.Result_RESULT_UNSPECIFIED && first.LogLength == 0 {
		return false
	} else if last := steps[len(steps)-1]; last.Result != runnerv1.Result_RESULT_UNSPECIFIED {
		return false
	}
	return true
}

// shouldAppendLogRow reports whether a non-raw_output entry should be written
// to the job log: only when we are between steps and the entry's level is
// within the globally configured log level.
func (r *Reporter) shouldAppendLogRow(entry *log.Entry) bool {
	return !r.duringSteps() && entry.Level <= log.GetLevel()
}

var stringToResult = map[string]runnerv1.Result{
	"success":   runnerv1.Result_RESULT_SUCCESS,
	"failure":   runnerv1.Result_RESULT_FAILURE,
	"skipped":   runnerv1.Result_RESULT_SKIPPED,
	"cancelled": runnerv1.Result_RESULT_CANCELLED,
}

func (r *Reporter) parseResult(result any) (runnerv1.Result, bool) {
	str := ""
	if v, ok := result.(string); ok { // for jobResult
		str = v
	} else if v, ok := result.(fmt.Stringer); ok { // for stepResult
		str = v.String()
	}

	ret, ok := stringToResult[str]
	return ret, ok
}

var cmdRegex = regexp.MustCompile(`^::([^ :]+)( .*)?::(.*)$`)

func (r *Reporter) handleCommand(originalContent, command, value string) *string {
	if r.stopCommandEndToken != "" && command != r.stopCommandEndToken {
		return &originalContent
	}

	switch command {
	case "add-mask":
		r.addMask(value)
		return nil
	case "debug":
		if r.debugOutputEnabled {
			return &value
		}
		return nil

	case "notice":
		// Not implemented yet, so just return the original content.
		return &originalContent
	case "warning":
		// Not implemented yet, so just return the original content.
		return &originalContent
	case "error":
		// Not implemented yet, so just return the original content.
		return &originalContent
	case "group":
		// Returning the original content, because I think the frontend
		// will use it when rendering the output.
		return &originalContent
	case "endgroup":
		// Ditto
		return &originalContent
	case "stop-commands":
		r.stopCommandEndToken = value
		return nil
	case r.stopCommandEndToken:
		r.stopCommandEndToken = ""
		return nil
	}
	return &originalContent
}

func (r *Reporter) parseLogRow(entry *log.Entry) *runnerv1.LogRow {
	content := strings.TrimRight(entry.Message, "\r\n")

	matches := cmdRegex.FindStringSubmatch(content)
	if matches != nil {
		if output := r.handleCommand(content, matches[1], matches[3]); output != nil {
			content = *output
		} else {
			return nil
		}
	}

	content = r.logReplacer.Replace(content)

	return &runnerv1.LogRow{
		Time:    timestamppb.New(entry.Time),
		Content: strings.ToValidUTF8(content, "?"),
	}
}

func (r *Reporter) addMask(msg string) {
	r.oldnew = append(r.oldnew, msg, "***")
	r.logReplacer = strings.NewReplacer(r.oldnew...)
}
