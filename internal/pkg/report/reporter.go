// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package report

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	runnerv1 "code.gitea.io/actions-proto-go/runner/v1"
	"connectrpc.com/connect"
	"github.com/avast/retry-go/v4"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"gitea.com/gitea/act_runner/internal/pkg/client"
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

	state   *runnerv1.TaskState
	stateMu sync.RWMutex
	outputs sync.Map

	debugOutputEnabled  bool
	stopCommandEndToken string
}

func NewReporter(ctx context.Context, cancel context.CancelFunc, client client.Client, task *runnerv1.Task) *Reporter {
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
		ctx:         ctx,
		cancel:      cancel,
		client:      client,
		oldnew:      oldnew,
		logReplacer: strings.NewReplacer(oldnew...),
		state: &runnerv1.TaskState{
			Id: task.Id,
		},
	}

	if task.Secrets["ACTIONS_STEP_DEBUG"] == "true" {
		rv.debugOutputEnabled = true
	}

	return rv
}

func (r *Reporter) ResetSteps(l int) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	for i := 0; i < l; i++ {
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

func (r *Reporter) Fire(entry *log.Entry) error {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	log.WithFields(entry.Data).Trace(entry.Message)

	timestamp := entry.Time
	if r.state.StartedAt == nil {
		r.state.StartedAt = timestamppb.New(timestamp)
	}

	stage := entry.Data["stage"]

	if stage != "Main" {
		if v, ok := entry.Data["jobResult"]; ok {
			if jobResult, ok := r.parseResult(v); ok {
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
			}
		}
		if !r.duringSteps() {
			r.logRows = appendIfNotNil(r.logRows, r.parseLogRow(entry))
		}
		return nil
	}

	var step *runnerv1.StepState
	if v, ok := entry.Data["stepNumber"]; ok {
		if v, ok := v.(int); ok && len(r.state.Steps) > v {
			step = r.state.Steps[v]
		}
	}
	if step == nil {
		if !r.duringSteps() {
			r.logRows = appendIfNotNil(r.logRows, r.parseLogRow(entry))
		}
		return nil
	}

	if step.StartedAt == nil {
		step.StartedAt = timestamppb.New(timestamp)
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
	} else if !r.duringSteps() {
		r.logRows = appendIfNotNil(r.logRows, r.parseLogRow(entry))
	}
	if v, ok := entry.Data["stepResult"]; ok {
		if stepResult, ok := r.parseResult(v); ok {
			if step.LogLength == 0 {
				step.LogIndex = int64(r.logOffset + len(r.logRows))
			}
			step.Result = stepResult
			step.StoppedAt = timestamppb.New(timestamp)
		}
	}

	return nil
}

func (r *Reporter) RunDaemon() {
	if r.closed {
		return
	}
	if r.ctx.Err() != nil {
		return
	}

	_ = r.ReportLog(false)
	_ = r.ReportState()

	time.AfterFunc(time.Second, r.RunDaemon)
}

func (r *Reporter) Logf(format string, a ...interface{}) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	r.logf(format, a...)
}

func (r *Reporter) logf(format string, a ...interface{}) {
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
	r.closed = true

	r.stateMu.Lock()
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

	return retry.Do(func() error {
		if err := r.ReportLog(true); err != nil {
			return err
		}
		return r.ReportState()
	}, retry.Context(r.ctx))
}

func (r *Reporter) ReportLog(noMore bool) error {
	r.clientM.Lock()
	defer r.clientM.Unlock()

	r.stateMu.RLock()
	rows := r.logRows
	r.stateMu.RUnlock()

	resp, err := r.client.UpdateLog(r.ctx, connect.NewRequest(&runnerv1.UpdateLogRequest{
		TaskId: r.state.Id,
		Index:  int64(r.logOffset),
		Rows:   rows,
		NoMore: noMore,
	}))
	if err != nil {
		return err
	}

	ack := int(resp.Msg.AckIndex)
	if ack < r.logOffset {
		return fmt.Errorf("submitted logs are lost")
	}

	r.stateMu.Lock()
	r.logRows = r.logRows[ack-r.logOffset:]
	r.logOffset = ack
	r.stateMu.Unlock()

	if noMore && ack < r.logOffset+len(rows) {
		return fmt.Errorf("not all logs are submitted")
	}

	return nil
}

func (r *Reporter) ReportState() error {
	r.clientM.Lock()
	defer r.clientM.Unlock()

	r.stateMu.RLock()
	state := proto.Clone(r.state).(*runnerv1.TaskState)
	r.stateMu.RUnlock()

	outputs := make(map[string]string)
	r.outputs.Range(func(k, v interface{}) bool {
		if val, ok := v.(string); ok {
			outputs[k.(string)] = val
		}
		return true
	})

	resp, err := r.client.UpdateTask(r.ctx, connect.NewRequest(&runnerv1.UpdateTaskRequest{
		State:   state,
		Outputs: outputs,
	}))
	if err != nil {
		return err
	}

	for _, k := range resp.Msg.SentOutputs {
		r.outputs.Store(k, struct{}{})
	}

	if resp.Msg.State != nil && resp.Msg.State.Result == runnerv1.Result_RESULT_CANCELLED {
		r.cancel()
	}

	var noSent []string
	r.outputs.Range(func(k, v interface{}) bool {
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

var stringToResult = map[string]runnerv1.Result{
	"success":   runnerv1.Result_RESULT_SUCCESS,
	"failure":   runnerv1.Result_RESULT_FAILURE,
	"skipped":   runnerv1.Result_RESULT_SKIPPED,
	"cancelled": runnerv1.Result_RESULT_CANCELLED,
}

func (r *Reporter) parseResult(result interface{}) (runnerv1.Result, bool) {
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

func (r *Reporter) handleCommand(originalContent, command, parameters, value string) *string {
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
	content := strings.TrimRightFunc(entry.Message, func(r rune) bool { return r == '\r' || r == '\n' })

	matches := cmdRegex.FindStringSubmatch(content)
	if matches != nil {
		if output := r.handleCommand(content, matches[1], matches[2], matches[3]); output != nil {
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
