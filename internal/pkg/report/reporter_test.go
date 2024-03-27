// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package report

import (
	"context"
	"strings"
	"testing"

	runnerv1 "code.gitea.io/actions-proto-go/runner/v1"
	connect_go "connectrpc.com/connect"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"gitea.com/gitea/act_runner/internal/pkg/client/mocks"
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
		taskCtx, err := structpb.NewStruct(map[string]interface{}{})
		require.NoError(t, err)
		reporter := NewReporter(ctx, cancel, client, &runnerv1.Task{
			Context: taskCtx,
		})
		defer func() {
			assert.NoError(t, reporter.Close(""))
		}()
		reporter.ResetSteps(5)

		dataStep0 := map[string]interface{}{
			"stage":      "Main",
			"stepNumber": 0,
			"raw_output": true,
		}

		assert.NoError(t, reporter.Fire(&log.Entry{Message: "regular log line", Data: dataStep0}))
		assert.NoError(t, reporter.Fire(&log.Entry{Message: "::debug::debug log line", Data: dataStep0}))
		assert.NoError(t, reporter.Fire(&log.Entry{Message: "regular log line", Data: dataStep0}))
		assert.NoError(t, reporter.Fire(&log.Entry{Message: "::debug::debug log line", Data: dataStep0}))
		assert.NoError(t, reporter.Fire(&log.Entry{Message: "::debug::debug log line", Data: dataStep0}))
		assert.NoError(t, reporter.Fire(&log.Entry{Message: "regular log line", Data: dataStep0}))

		assert.Equal(t, int64(3), reporter.state.Steps[0].LogLength)
	})
}
