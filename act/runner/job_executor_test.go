// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/container"
	"gitea.com/gitea/runner/act/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestJobExecutor(t *testing.T) {
	// Dryrun only checks syntax/planning; all cases resolve locally, so this runs offline.
	tables := []TestJobFileInfo{
		{workdir, "uses-and-run-in-one-step", "push", "Invalid run/uses syntax for job:test step:Test", platforms, secrets},
		{workdir, "uses-github-empty", "push", "Expected format {org}/{repo}[/path]@ref", platforms, secrets},
		{workdir, "uses-github-noref", "push", "Expected format {org}/{repo}[/path]@ref", platforms, secrets},
		{workdir, "uses-github-root", "push", "", platforms, secrets},
		{workdir, "uses-docker-url", "push", "", platforms, secrets},
		{workdir, "job-nil-step", "push", "invalid Step 0: missing run or uses key", platforms, secrets},
	}
	// These tests are sufficient to only check syntax.
	ctx := common.WithDryrun(context.Background(), true)
	for _, table := range tables {
		t.Run(table.workflowPath, func(t *testing.T) {
			table.runTest(ctx, t, &Config{})
		})
	}
}

type jobInfoMock struct {
	mock.Mock
}

func (jim *jobInfoMock) matrix() map[string]any {
	args := jim.Called()
	return args.Get(0).(map[string]any)
}

func (jim *jobInfoMock) steps() []*model.Step {
	args := jim.Called()

	return args.Get(0).([]*model.Step)
}

func (jim *jobInfoMock) startContainer() common.Executor {
	args := jim.Called()

	return args.Get(0).(func(context.Context) error)
}

func (jim *jobInfoMock) stopContainer() common.Executor {
	args := jim.Called()

	return args.Get(0).(func(context.Context) error)
}

func (jim *jobInfoMock) closeContainer() common.Executor {
	args := jim.Called()

	return args.Get(0).(func(context.Context) error)
}

func (jim *jobInfoMock) interpolateOutputs() common.Executor {
	args := jim.Called()

	return args.Get(0).(func(context.Context) error)
}

func (jim *jobInfoMock) result(result string) {
	jim.Called(result)
}

type jobContainerMock struct {
	container.Container
	container.LinuxContainerEnvironmentExtensions
}

func (jcm *jobContainerMock) ReplaceLogWriter(_, _ io.Writer) (io.Writer, io.Writer) {
	return nil, nil
}

type stepFactoryMock struct {
	mock.Mock
}

func (sfm *stepFactoryMock) newStep(model *model.Step, rc *RunContext) (step, error) {
	args := sfm.Called(model, rc)
	return args.Get(0).(step), args.Error(1)
}

func TestNewJobExecutor(t *testing.T) {
	table := []struct {
		name          string
		steps         []*model.Step
		preSteps      []bool
		postSteps     []bool
		executedSteps []string
		result        string
		hasError      bool
	}{
		{
			name:          "zeroSteps",
			steps:         []*model.Step{},
			preSteps:      []bool{},
			postSteps:     []bool{},
			executedSteps: []string{},
			result:        "success",
			hasError:      false,
		},
		{
			name: "stepWithoutPrePost",
			steps: []*model.Step{{
				ID: "1",
			}},
			preSteps:  []bool{false},
			postSteps: []bool{false},
			executedSteps: []string{
				"startContainer",
				"step1",
				"stopContainer",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "success",
			hasError: false,
		},
		{
			name: "stepWithFailure",
			steps: []*model.Step{{
				ID: "1",
			}},
			preSteps:  []bool{false},
			postSteps: []bool{false},
			executedSteps: []string{
				"startContainer",
				"step1",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "failure",
			hasError: true,
		},
		{
			name: "stepWithPre",
			steps: []*model.Step{{
				ID: "1",
			}},
			preSteps:  []bool{true},
			postSteps: []bool{false},
			executedSteps: []string{
				"startContainer",
				"pre1",
				"step1",
				"stopContainer",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "success",
			hasError: false,
		},
		{
			name: "stepWithPost",
			steps: []*model.Step{{
				ID: "1",
			}},
			preSteps:  []bool{false},
			postSteps: []bool{true},
			executedSteps: []string{
				"startContainer",
				"step1",
				"post1",
				"stopContainer",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "success",
			hasError: false,
		},
		{
			name: "stepWithPreAndPost",
			steps: []*model.Step{{
				ID: "1",
			}},
			preSteps:  []bool{true},
			postSteps: []bool{true},
			executedSteps: []string{
				"startContainer",
				"pre1",
				"step1",
				"post1",
				"stopContainer",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "success",
			hasError: false,
		},
		{
			name: "stepsWithPreAndPost",
			steps: []*model.Step{{
				ID: "1",
			}, {
				ID: "2",
			}, {
				ID: "3",
			}},
			preSteps:  []bool{true, false, true},
			postSteps: []bool{false, true, true},
			executedSteps: []string{
				"startContainer",
				"pre1",
				"pre3",
				"step1",
				"step2",
				"step3",
				"post3",
				"post2",
				"stopContainer",
				"interpolateOutputs",
				"closeContainer",
			},
			result:   "success",
			hasError: false,
		},
	}

	contains := func(needle string, haystack []string) bool {
		return slices.Contains(haystack, needle)
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			fmt.Printf("::group::%s\n", tt.name) //nolint:forbidigo // pre-existing issue from nektos/act

			ctx := common.WithJobErrorContainer(context.Background())
			jim := &jobInfoMock{}
			sfm := &stepFactoryMock{}
			rc := &RunContext{
				JobContainer: &jobContainerMock{},
				Run: &model.Run{
					JobID: "test",
					Workflow: &model.Workflow{
						Jobs: map[string]*model.Job{
							"test": {},
						},
					},
				},
				Config: &Config{},
			}
			rc.ExprEval = rc.NewExpressionEvaluator(ctx)
			executorOrder := make([]string, 0)

			jim.On("steps").Return(tt.steps)

			if len(tt.steps) > 0 {
				jim.On("startContainer").Return(func(ctx context.Context) error {
					executorOrder = append(executorOrder, "startContainer")
					return nil
				})
			}

			for i, stepModel := range tt.steps {
				sm := &stepMock{}

				sfm.On("newStep", stepModel, rc).Return(sm, nil)

				sm.On("pre").Return(func(ctx context.Context) error {
					if tt.preSteps[i] {
						executorOrder = append(executorOrder, "pre"+stepModel.ID)
					}
					return nil
				})

				sm.On("main").Return(func(ctx context.Context) error {
					executorOrder = append(executorOrder, "step"+stepModel.ID)
					if tt.hasError {
						return errors.New("error")
					}
					return nil
				})

				sm.On("post").Return(func(ctx context.Context) error {
					if tt.postSteps[i] {
						executorOrder = append(executorOrder, "post"+stepModel.ID)
					}
					return nil
				})

				defer sm.AssertExpectations(t)
			}

			if len(tt.steps) > 0 {
				jim.On("matrix").Return(map[string]any{})

				jim.On("interpolateOutputs").Return(func(ctx context.Context) error {
					executorOrder = append(executorOrder, "interpolateOutputs")
					return nil
				})

				if contains("stopContainer", tt.executedSteps) {
					jim.On("stopContainer").Return(func(ctx context.Context) error {
						executorOrder = append(executorOrder, "stopContainer")
						return nil
					})
				}

				jim.On("result", tt.result)

				jim.On("closeContainer").Return(func(ctx context.Context) error {
					executorOrder = append(executorOrder, "closeContainer")
					return nil
				})
			}

			executor := newJobExecutor(jim, sfm, rc)
			err := executor(ctx)
			assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
			assert.Equal(t, tt.executedSteps, executorOrder)

			jim.AssertExpectations(t)
			sfm.AssertExpectations(t)

			fmt.Println("::endgroup::") //nolint:forbidigo // pre-existing issue from nektos/act
		})
	}
}
