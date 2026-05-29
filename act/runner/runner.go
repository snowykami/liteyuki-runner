// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"runtime"
	"sync"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/model"

	docker_container "github.com/moby/moby/api/types/container"
	log "github.com/sirupsen/logrus"
)

// Runner provides capabilities to run GitHub actions
type Runner interface {
	NewPlanExecutor(plan *model.Plan) common.Executor
}

// Config contains the config for a new runner
type Config struct {
	Actor                              string                       // the user that triggered the event
	Workdir                            string                       // path to working directory
	ActionCacheDir                     string                       // path used for caching action contents
	ActionOfflineMode                  bool                         // when offline, use cached action contents
	BindWorkdir                        bool                         // bind the workdir to the job container
	EventName                          string                       // name of event to run
	EventPath                          string                       // path to JSON file to use for event.json in containers
	DefaultBranch                      string                       // name of the main branch for this repository
	ReuseContainers                    bool                         // reuse containers to maintain state
	ForcePull                          bool                         // force pulling of the image, even if already present
	ForceRebuild                       bool                         // force rebuilding local docker image action
	LogOutput                          bool                         // log the output from docker run
	JSONLogger                         bool                         // use json or text logger
	LogPrefixJobID                     bool                         // switches from the full job name to the job id
	Env                                map[string]string            // env for containers
	Inputs                             map[string]string            // manually passed action inputs
	Secrets                            map[string]string            // list of secrets
	Vars                               map[string]string            // list of vars
	Token                              string                       // GitHub token
	InsecureSecrets                    bool                         // switch hiding output when printing to terminal
	Platforms                          map[string]string            // list of platforms
	Privileged                         bool                         // use privileged mode
	UsernsMode                         string                       // user namespace to use
	ContainerArchitecture              string                       // Desired OS/architecture platform for running containers
	ContainerDaemonSocket              string                       // Path to Docker daemon socket
	ContainerOptions                   string                       // Options for the job container
	UseGitIgnore                       bool                         // controls if paths in .gitignore should not be copied into container, default true
	GitHubInstance                     string                       // GitHub instance to use, default "github.com"
	ContainerCapAdd                    []string                     // list of kernel capabilities to add to the containers
	ContainerCapDrop                   []string                     // list of kernel capabilities to remove from the containers
	AutoRemove                         bool                         // controls if the container is automatically removed upon workflow completion
	ArtifactServerPath                 string                       // the path where the artifact server stores uploads
	ArtifactServerAddr                 string                       // the address the artifact server binds to
	ArtifactServerPort                 string                       // the port the artifact server binds to
	NoSkipCheckout                     bool                         // do not skip actions/checkout
	RemoteName                         string                       // remote name in local git repo config
	ReplaceGheActionWithGithubCom      []string                     // Use actions from GitHub Enterprise instance to GitHub
	ReplaceGheActionTokenWithGithubCom string                       // Token of private action repo on GitHub.
	Matrix                             map[string]map[string]bool   // Matrix config to run
	ContainerNetworkMode               docker_container.NetworkMode // the network mode of job containers (the value of --network)
	ActionCache                        ActionCache                  // Use a custom ActionCache Implementation

	PresetGitHubContext   *model.GithubContext         // the preset github context, overrides some fields like DefaultBranch, Env, Secrets etc.
	EventJSON             string                       // the content of JSON file to use for event.json in containers, overrides EventPath
	ContainerNamePrefix   string                       // the prefix of container name
	ContainerMaxLifetime  time.Duration                // the max lifetime of job containers
	CleanWorkdir          bool                         // remove host executor workdir on teardown
	DefaultActionInstance string                       // the default actions web site
	PlatformPicker        func(labels []string) string // platform picker, it will take precedence over Platforms if isn't nil
	JobLoggerLevel        *log.Level                   // the level of job logger
	ValidVolumes          []string                     // only volumes (and bind mounts) in this slice can be mounted on the job container or service containers
	InsecureSkipTLS       bool                         // whether to skip verifying TLS certificate of the Gitea instance
	MaxParallel           int                          // max parallel jobs to run across all workflows (0 = no limit, uses CPU count)
	AllocatePTY           bool                         // allocate a pseudo-TTY for each step's process
}

// GetToken: Adapt to Gitea
func (c Config) GetToken() string {
	token := c.Secrets["GITHUB_TOKEN"]
	if c.Secrets["GITEA_TOKEN"] != "" {
		token = c.Secrets["GITEA_TOKEN"]
	}
	return token
}

// DefaultActionURL returns the host used for implicit remote actions.
func (c Config) DefaultActionURL() string {
	if c.DefaultActionInstance != "" {
		return c.DefaultActionInstance
	}
	if c.GitHubInstance != "" {
		return c.GitHubInstance
	}
	return "github.com"
}

type caller struct {
	runContext *RunContext

	updateResultLock         sync.Mutex        // For Gitea
	reusedWorkflowJobResults map[string]string // For Gitea
}

type runnerImpl struct {
	config    *Config
	eventJSON string
	caller    *caller // the job calling this runner (caller of a reusable workflow)
}

// New Creates a new Runner
func New(runnerConfig *Config) (Runner, error) {
	runner := &runnerImpl{
		config: runnerConfig,
	}

	return runner.configure()
}

func (runner *runnerImpl) configure() (Runner, error) {
	runner.eventJSON = "{}"
	if runner.config.EventJSON != "" {
		runner.eventJSON = runner.config.EventJSON
	} else if runner.config.EventPath != "" {
		log.Debugf("Reading event.json from %s", runner.config.EventPath)
		eventJSONBytes, err := os.ReadFile(runner.config.EventPath)
		if err != nil {
			return nil, err
		}
		runner.eventJSON = string(eventJSONBytes)
	} else if len(runner.config.Inputs) != 0 {
		eventMap := map[string]map[string]string{
			"inputs": runner.config.Inputs,
		}
		eventJSON, err := json.Marshal(eventMap)
		if err != nil {
			return nil, err
		}
		runner.eventJSON = string(eventJSON)
	}
	return runner, nil
}

// NewPlanExecutor ...
func (runner *runnerImpl) NewPlanExecutor(plan *model.Plan) common.Executor {
	maxJobNameLen := 0

	stagePipeline := make([]common.Executor, 0)
	log.Debugf("Plan Stages: %v", plan.Stages)

	for i := range plan.Stages {
		stage := plan.Stages[i]
		stagePipeline = append(stagePipeline, func(ctx context.Context) error {
			pipeline := make([]common.Executor, 0)
			for _, run := range stage.Runs {
				log.Debugf("Stages Runs: %v", stage.Runs)
				stageExecutor := make([]common.Executor, 0)
				job := run.Job()
				log.Debugf("Job.Name: %v", job.Name)
				log.Debugf("Job.RawNeeds: %v", job.RawNeeds)
				log.Debugf("Job.RawRunsOn: %v", job.RawRunsOn)
				log.Debugf("Job.Env: %v", job.Env)
				log.Debugf("Job.If: %v", job.If)
				for step := range job.Steps {
					if nil != job.Steps[step] {
						log.Debugf("Job.Steps: %v", job.Steps[step].String())
					}
				}
				log.Debugf("Job.TimeoutMinutes: %v", job.TimeoutMinutes)
				log.Debugf("Job.Services: %v", job.Services)
				log.Debugf("Job.Strategy: %v", job.Strategy)
				log.Debugf("Job.RawContainer: %v", job.RawContainer)
				log.Debugf("Job.Defaults.Run.Shell: %v", job.Defaults.Run.Shell)
				log.Debugf("Job.Defaults.Run.WorkingDirectory: %v", job.Defaults.Run.WorkingDirectory)
				log.Debugf("Job.Outputs: %v", job.Outputs)
				log.Debugf("Job.Uses: %v", job.Uses)
				log.Debugf("Job.With: %v", job.With)
				// log.Debugf("Job.RawSecrets: %v", job.RawSecrets)
				log.Debugf("Job.Result: %v", job.Result)

				if job.Strategy != nil {
					log.Debugf("Job.Strategy.FailFast: %v", job.Strategy.FailFast)
					log.Debugf("Job.Strategy.MaxParallel: %v", job.Strategy.MaxParallel)
					log.Debugf("Job.Strategy.FailFastString: %v", job.Strategy.FailFastString)
					log.Debugf("Job.Strategy.MaxParallelString: %v", job.Strategy.MaxParallelString)
					log.Debugf("Job.Strategy.RawMatrix: %v", job.Strategy.RawMatrix)

					strategyRc := runner.newRunContext(ctx, run, nil)
					// Resolve template expressions in the matrix node before Matrix() is called.
					// On failure the literal string is kept and normalizeMatrixValue wraps it as a fallback.
					if err := strategyRc.NewExpressionEvaluator(ctx).EvaluateYamlNode(ctx, &job.Strategy.RawMatrix); err != nil {
						log.Errorf("Error while evaluating matrix: %v", err)
					}
				}

				var matrixes []map[string]any
				if m, err := job.GetMatrixes(); err != nil {
					log.Errorf("Error while get job's matrix: %v", err)
				} else {
					log.Debugf("Job Matrices: %v", m)
					log.Debugf("Runner Matrices: %v", runner.config.Matrix)
					matrixes = selectMatrixes(m, runner.config.Matrix)
				}
				log.Debugf("Final matrix after applying user inclusions '%v'", matrixes)

				maxParallel := 4
				if job.Strategy != nil {
					// Ensure GetMaxParallel() is called if MaxParallel is still 0
					if job.Strategy.MaxParallel == 0 {
						job.Strategy.MaxParallel = job.Strategy.GetMaxParallel()
					}
					maxParallel = job.Strategy.MaxParallel
					log.Debugf("Using job.Strategy.MaxParallel: %d", maxParallel)
				}

				if len(matrixes) < maxParallel {
					log.Debugf("Adjusting maxParallel from %d to %d (number of matrix combinations)", maxParallel, len(matrixes))
					maxParallel = len(matrixes)
				}

				log.Infof("Running job with maxParallel=%d for %d matrix combinations", maxParallel, len(matrixes))

				for i, matrix := range matrixes {
					rc := runner.newRunContext(ctx, run, matrix)
					rc.JobName = rc.Name
					if len(matrixes) > 1 {
						rc.Name = fmt.Sprintf("%s-%d", rc.Name, i+1)
					}
					if len(rc.String()) > maxJobNameLen {
						maxJobNameLen = len(rc.String())
					}
					if rc.caller != nil { // For Gitea
						rc.caller.setReusedWorkflowJobResult(rc.JobName, "pending")
					}
					stageExecutor = append(stageExecutor, func(ctx context.Context) error {
						jobName := fmt.Sprintf("%-*s", maxJobNameLen, rc.String())
						executor, err := rc.Executor()
						if err != nil {
							return err
						}

						return executor(common.WithJobErrorContainer(WithJobLogger(ctx, rc.Run.JobID, jobName, rc.Config, &rc.Masks, matrix)))
					})
				}
				// Run all matrix combinations of this job, then drop its aggregation mutex: the
				// combos are the only users of it, so once they finish the jobMutexes entry can be
				// released, keeping the map from growing unbounded over a long-lived runner.
				stageParallel := common.NewParallelExecutor(maxParallel, stageExecutor...)
				pipeline = append(pipeline, func(ctx context.Context) error {
					defer jobMutexes.Delete(job)
					return stageParallel(ctx)
				})
			}

			// For pipeline execution:
			// - If only 1 element: run it directly (no need for additional parallelization)
			// - If multiple elements: run them in parallel up to maxParallel or ncpu
			if len(pipeline) == 0 {
				return nil
			}

			if len(pipeline) == 1 {
				// Single run/job: execute directly without additional parallelization wrapper
				// This ensures max-parallel is the only limiting factor
				log.Debugf("Single pipeline element, executing directly")
				return pipeline[0](ctx)
			}

			// Multiple runs/jobs: execute in parallel up to maxParallel (if set) or ncpu
			parallelism := runtime.NumCPU()

			// If MaxParallel is set in config, use it
			if runner.config.MaxParallel > 0 {
				parallelism = runner.config.MaxParallel
				log.Debugf("Using configured max-parallel: %d", parallelism)
			} else {
				log.Debugf("Using CPU count for parallelism: %d", parallelism)
			}

			// Don't exceed the number of pipeline elements
			if parallelism > len(pipeline) {
				parallelism = len(pipeline)
			}

			log.Infof("Executing %d pipeline elements with parallelism %d", len(pipeline), parallelism)
			return common.NewParallelExecutor(parallelism, pipeline...)(ctx)
		})
	}

	return common.NewPipelineExecutor(stagePipeline...).Then(handleFailure(plan))
}

func handleFailure(plan *model.Plan) common.Executor {
	return func(ctx context.Context) error {
		for _, stage := range plan.Stages {
			for _, run := range stage.Runs {
				if run.Job().Result == "failure" {
					return fmt.Errorf("Job '%s' failed", run.String())
				}
			}
		}
		return nil
	}
}

func selectMatrixes(originalMatrixes []map[string]any, targetMatrixValues map[string]map[string]bool) []map[string]any {
	matrixes := make([]map[string]any, 0)
	for _, original := range originalMatrixes {
		flag := true
		for key, val := range original {
			if allowedVals, ok := targetMatrixValues[key]; ok {
				valToString := fmt.Sprintf("%v", val)
				if _, ok := allowedVals[valToString]; !ok {
					flag = false
				}
			}
		}
		if flag {
			matrixes = append(matrixes, original)
		}
	}
	return matrixes
}

func (runner *runnerImpl) newRunContext(ctx context.Context, run *model.Run, matrix map[string]any) *RunContext {
	rc := &RunContext{
		Config:      runner.config,
		Run:         run,
		EventJSON:   runner.eventJSON,
		StepResults: make(map[string]*model.StepResult),
		Matrix:      matrix,
		caller:      runner.caller,
	}
	rc.ExprEval = rc.NewExpressionEvaluator(ctx)
	rc.Name = rc.ExprEval.Interpolate(ctx, run.String())
	// Snapshot the job's pristine output expressions now, before any matrix combo runs and
	// rewrites the shared Job.Outputs (see interpolateOutputs).
	if job := run.Job(); job != nil {
		rc.outputTemplate = maps.Clone(job.Outputs)
	}

	return rc
}

// For Gitea
func (c *caller) setReusedWorkflowJobResult(jobName, result string) {
	c.updateResultLock.Lock()
	defer c.updateResultLock.Unlock()
	c.reusedWorkflowJobResults[jobName] = result
}
