// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/model"
)

type jobInfo interface {
	matrix() map[string]any
	steps() []*model.Step
	startContainer() common.Executor
	stopContainer() common.Executor
	closeContainer() common.Executor
	interpolateOutputs() common.Executor
	result(result string)
}

// reportStepError emits the GitHub Actions ##[error] annotation and records
// the error against the job so the job is reported as failed.
func reportStepError(ctx context.Context, err error) {
	common.Logger(ctx).Errorf("##[error]%v", err)
	common.SetJobError(ctx, err)
}

func newJobExecutor(info jobInfo, sf stepFactory, rc *RunContext) common.Executor {
	steps := make([]common.Executor, 0)
	preSteps := make([]common.Executor, 0)
	var postExecutor common.Executor

	steps = append(steps, func(ctx context.Context) error {
		logger := common.Logger(ctx)
		if len(info.matrix()) > 0 {
			logger.Infof("Matrix: %v", info.matrix())
		}
		return nil
	})

	infoSteps := info.steps()

	if len(infoSteps) == 0 {
		return common.NewDebugExecutor("No steps found")
	}

	preSteps = append(preSteps, func(ctx context.Context) error {
		// Have to be skipped for some Tests
		if rc.Run == nil {
			return nil
		}
		rc.ExprEval = rc.NewExpressionEvaluator(ctx)
		// evaluate environment variables since they can contain
		// GitHub's special environment variables.
		for k, v := range rc.GetEnv() {
			rc.Env[k] = rc.ExprEval.Interpolate(ctx, v)
		}
		return nil
	})

	for i, stepModel := range infoSteps {
		if stepModel == nil {
			return func(ctx context.Context) error {
				return fmt.Errorf("invalid Step %v: missing run or uses key", i)
			}
		}
		if stepModel.ID == "" {
			stepModel.ID = strconv.Itoa(i)
		}
		stepModel.Number = i

		step, err := sf.newStep(stepModel, rc)
		if err != nil {
			return common.NewErrorExecutor(err)
		}

		preExec := step.pre()
		preSteps = append(preSteps, useStepLogger(rc, stepModel, stepStagePre, func(ctx context.Context) error {
			preErr := preExec(ctx)
			if preErr != nil {
				reportStepError(ctx, preErr)
			} else if ctx.Err() != nil {
				reportStepError(ctx, ctx.Err())
			}
			return preErr
		}))

		stepExec := step.main()
		steps = append(steps, useStepLogger(rc, stepModel, stepStageMain, func(ctx context.Context) error {
			err := stepExec(ctx)
			if err != nil {
				reportStepError(ctx, err)
			} else if ctx.Err() != nil {
				reportStepError(ctx, ctx.Err())
			}
			return nil
		}))

		postFn := step.post()
		postExec := useStepLogger(rc, stepModel, stepStagePost, func(ctx context.Context) error {
			err := postFn(ctx)
			if err != nil {
				reportStepError(ctx, err)
			} else if ctx.Err() != nil {
				reportStepError(ctx, ctx.Err())
			}
			return err
		})
		if postExecutor != nil {
			// run the post executor in reverse order
			postExecutor = postExec.Finally(postExecutor)
		} else {
			postExecutor = postExec
		}
	}

	postExecutor = postExecutor.Finally(func(ctx context.Context) error {
		jobError := common.JobError(ctx)
		var err error
		if rc.Config.AutoRemove || jobError == nil {
			// always allow 1 min for stopping and removing the runner, even if we were cancelled
			ctx, cancel := context.WithTimeout(common.WithLogger(context.Background(), common.Logger(ctx)), time.Minute)
			defer cancel()

			logger := common.Logger(ctx)
			// For Gitea
			// We don't need to call `stopServiceContainers` here since it will be called by following `info.stopContainer`
			// logger.Infof("Cleaning up services for job %s", rc.JobName)
			// if err := rc.stopServiceContainers()(ctx); err != nil {
			// 	logger.Errorf("Error while cleaning services: %v", err)
			// }

			logger.Infof("Cleaning up container for job %s", rc.JobName)
			if err = info.stopContainer()(ctx); err != nil {
				logger.Errorf("Error while stop job container: %v", err)
			}

			// For Gitea
			// We don't need to call `NewDockerNetworkRemoveExecutor` here since it is called by above `info.stopContainer`
			// if !rc.IsHostEnv(ctx) && rc.Config.ContainerNetworkMode == "" {
			// 	// clean network in docker mode only
			// 	// if the value of `ContainerNetworkMode` is empty string,
			// 	// it means that the network to which containers are connecting is created by `runner`,
			// 	// so, we should remove the network at last.
			// 	networkName, _ := rc.networkName()
			// 	logger.Infof("Cleaning up network for job %s, and network name is: %s", rc.JobName, networkName)
			// 	if err := container.NewDockerNetworkRemoveExecutor(networkName)(ctx); err != nil {
			// 		logger.Errorf("Error while cleaning network: %v", err)
			// 	}
			// }
		}
		setJobResult(ctx, info, rc, jobError == nil)
		setJobOutputs(ctx, rc)

		return err
	})

	pipeline := make([]common.Executor, 0)
	pipeline = append(pipeline, preSteps...)
	pipeline = append(pipeline, steps...)

	return common.NewPipelineExecutor(info.startContainer(), common.NewPipelineExecutor(pipeline...).
		Finally(func(ctx context.Context) error {
			var cancel context.CancelFunc
			if ctx.Err() == context.Canceled {
				// in case of an aborted run, we still should execute the
				// post steps to allow cleanup.
				ctx, cancel = context.WithTimeout(common.WithLogger(context.Background(), common.Logger(ctx)), 5*time.Minute)
				defer cancel()
			}
			return postExecutor(ctx)
		}).
		Finally(info.interpolateOutputs()).
		Finally(info.closeContainer()))
}

func setJobResult(ctx context.Context, info jobInfo, rc *RunContext, success bool) {
	logger := common.Logger(ctx)

	// Matrix combinations share one *model.Job and run in parallel; serialize the
	// read-modify-write of the job result so a failing combination is not lost-updated by a
	// concurrent succeeding one.
	job := rc.Run.Job()
	jobResult := func() string {
		defer lockJob(job)()
		result := "success"
		// we have only one result for a whole matrix build, so we need
		// to keep an existing result state if we run a matrix
		if len(info.matrix()) > 0 && job.Result != "" {
			result = job.Result
		}
		if !success {
			result = "failure"
		}
		info.result(result)
		return result
	}()

	if rc.caller != nil {
		// set reusable workflow job result
		rc.caller.setReusedWorkflowJobResult(rc.JobName, jobResult) // For Gitea
		return
	}

	jobResultMessage := "succeeded"
	if jobResult != "success" {
		jobResultMessage = "failed"
	}

	logger.WithField("jobResult", jobResult).Infof("Job %s", jobResultMessage)
}

func setJobOutputs(ctx context.Context, rc *RunContext) {
	if rc.caller != nil {
		// map outputs for reusable workflows
		callerOutputs := make(map[string]string)

		ee := rc.NewExpressionEvaluator(ctx)

		for k, v := range rc.Run.Workflow.WorkflowCallConfig().Outputs {
			callerOutputs[k] = ee.Interpolate(ctx, ee.Interpolate(ctx, v.Value))
		}

		// Matrix combinations of a reusable-workflow caller share the caller's *model.Job;
		// serialize the write so parallel combos don't race on its Outputs field.
		callerJob := rc.caller.runContext.Run.Job()
		defer lockJob(callerJob)()
		callerJob.Outputs = callerOutputs
	}
}

func useStepLogger(rc *RunContext, stepModel *model.Step, stage stepStage, executor common.Executor) common.Executor {
	return func(ctx context.Context) error {
		ctx = withStepLogger(ctx, stepModel.Number, stepModel.ID, rc.ExprEval.Interpolate(ctx, stepModel.String()), stage.String())

		rawLogger := common.Logger(ctx).WithField("raw_output", true)
		logWriter := common.NewLineWriter(rc.commandHandler(ctx), func(s string) bool {
			if rc.Config.LogOutput {
				rawLogger.Infof("%s", s)
			} else {
				rawLogger.Debugf("%s", s)
			}
			return true
		})

		oldout, olderr := rc.JobContainer.ReplaceLogWriter(logWriter, logWriter)
		defer rc.JobContainer.ReplaceLogWriter(oldout, olderr)

		return executor(ctx)
	}
}
