// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"testing"

	runnerv1 "code.gitea.io/actions-proto-go/runner/v1"
	"github.com/nektos/act/pkg/model"
	"github.com/stretchr/testify/require"
	"gotest.tools/v3/assert"
)

func Test_generateWorkflow(t *testing.T) {
	type args struct {
		task *runnerv1.Task
	}
	tests := []struct {
		name    string
		args    args
		assert  func(t *testing.T, wf *model.Workflow)
		want1   string
		wantErr bool
	}{
		{
			name: "has needs",
			args: args{
				task: &runnerv1.Task{
					WorkflowPayload: []byte(`
name: Build and deploy
on: push

jobs:
  job9:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - run: ./deploy --build ${{ needs.job1.outputs.output1 }}
      - run: ./deploy --build ${{ needs.job2.outputs.output2 }}
`),
					Needs: map[string]*runnerv1.TaskNeed{
						"job1": {
							Outputs: map[string]string{
								"output1": "output1 value",
							},
							Result: runnerv1.Result_RESULT_SUCCESS,
						},
						"job2": {
							Outputs: map[string]string{
								"output2": "output2 value",
							},
							Result: runnerv1.Result_RESULT_SUCCESS,
						},
					},
				},
			},
			assert: func(t *testing.T, wf *model.Workflow) {
				assert.DeepEqual(t, wf.GetJob("job9").Needs(), []string{"job1", "job2"})
			},
			want1:   "job9",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := generateWorkflow(tt.args.task)
			require.NoError(t, err)
			tt.assert(t, got)
			assert.Equal(t, got1, tt.want1)
		})
	}
}
