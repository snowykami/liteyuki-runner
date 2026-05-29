// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/model"

	"github.com/joho/godotenv"
	log "github.com/sirupsen/logrus"
	assert "github.com/stretchr/testify/assert"
	"go.yaml.in/yaml/v4"
)

var (
	baseImage = "node:24-bookworm-slim"
	platforms map[string]string
	logLevel  = log.DebugLevel
	workdir   = "testdata"
	secrets   map[string]string
)

func init() {
	if p := os.Getenv("ACT_TEST_IMAGE"); p != "" {
		baseImage = p
	}

	platforms = map[string]string{
		"ubuntu-latest": baseImage,
	}

	if l := os.Getenv("ACT_TEST_LOG_LEVEL"); l != "" {
		if lvl, err := log.ParseLevel(l); err == nil {
			logLevel = lvl
		}
	}

	if wd, err := filepath.Abs(workdir); err == nil {
		workdir = wd
	}

	secrets = map[string]string{}
}

func TestNoWorkflowsFoundByPlanner(t *testing.T) {
	planner, err := model.NewWorkflowPlanner("res", true)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	out := log.StandardLogger().Out
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetLevel(log.DebugLevel)
	plan, err := planner.PlanEvent("pull_request")
	assert.NotNil(t, plan)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Contains(t, buf.String(), "no workflows found by planner")
	buf.Reset()
	plan, err = planner.PlanAll()
	assert.NotNil(t, plan)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Contains(t, buf.String(), "no workflows found by planner")
	log.SetOutput(out)
}

func TestGraphMissingEvent(t *testing.T) {
	planner, err := model.NewWorkflowPlanner("testdata/issue-1595/no-event.yml", true)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	out := log.StandardLogger().Out
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetLevel(log.DebugLevel)

	plan, err := planner.PlanEvent("push")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.NotNil(t, plan)
	assert.Empty(t, plan.Stages)

	assert.Contains(t, buf.String(), "no events found for workflow: no-event.yml")
	log.SetOutput(out)
}

func TestGraphMissingFirst(t *testing.T) {
	planner, err := model.NewWorkflowPlanner("testdata/issue-1595/no-first.yml", true)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	plan, err := planner.PlanEvent("push")
	assert.EqualError(t, err, "unable to build dependency graph for no first (no-first.yml)") //nolint:testifylint // pre-existing issue from nektos/act
	assert.NotNil(t, plan)
	assert.Empty(t, plan.Stages)
}

func TestGraphWithMissing(t *testing.T) {
	planner, err := model.NewWorkflowPlanner("testdata/issue-1595/missing.yml", true)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	out := log.StandardLogger().Out
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetLevel(log.DebugLevel)

	plan, err := planner.PlanEvent("push")
	assert.NotNil(t, plan)
	assert.Empty(t, plan.Stages)
	assert.EqualError(t, err, "unable to build dependency graph for missing (missing.yml)") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Contains(t, buf.String(), "unable to build dependency graph for missing (missing.yml)")
	log.SetOutput(out)
}

func TestGraphWithSomeMissing(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	planner, err := model.NewWorkflowPlanner("testdata/issue-1595/", true)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	out := log.StandardLogger().Out
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetLevel(log.DebugLevel)

	plan, err := planner.PlanAll()
	assert.Error(t, err, "unable to build dependency graph for no first (no-first.yml)") //nolint:testifylint // pre-existing issue from nektos/act
	assert.NotNil(t, plan)
	assert.Len(t, plan.Stages, 1)
	assert.Contains(t, buf.String(), "unable to build dependency graph for missing (missing.yml)")
	assert.Contains(t, buf.String(), "unable to build dependency graph for no first (no-first.yml)")
	log.SetOutput(out)
}

func TestGraphEvent(t *testing.T) {
	planner, err := model.NewWorkflowPlanner("testdata/basic", true)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	plan, err := planner.PlanEvent("push")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.NotNil(t, plan)
	assert.NotNil(t, plan.Stages)
	assert.Equal(t, len(plan.Stages), 3, "stages")                  //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, len(plan.Stages[0].Runs), 1, "stage0.runs")     //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, len(plan.Stages[1].Runs), 1, "stage1.runs")     //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, len(plan.Stages[2].Runs), 1, "stage2.runs")     //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, plan.Stages[0].Runs[0].JobID, "check", "jobid") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, plan.Stages[1].Runs[0].JobID, "build", "jobid") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, plan.Stages[2].Runs[0].JobID, "test", "jobid")  //nolint:testifylint // pre-existing issue from nektos/act

	plan, err = planner.PlanEvent("release")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.NotNil(t, plan)
	assert.Empty(t, plan.Stages)
}

type TestJobFileInfo struct {
	workdir      string
	workflowPath string
	eventName    string
	errorMessage string
	platforms    map[string]string
	secrets      map[string]string
}

func (j *TestJobFileInfo) runTest(ctx context.Context, t *testing.T, cfg *Config) {
	fmt.Printf("::group::%s\n", j.workflowPath) //nolint:forbidigo // pre-existing issue from nektos/act

	log.SetLevel(logLevel)

	workdir, err := filepath.Abs(j.workdir)
	assert.NoError(t, err, workdir) //nolint:testifylint // pre-existing issue from nektos/act

	fullWorkflowPath := filepath.Join(workdir, j.workflowPath)
	runnerConfig := &Config{
		Workdir:               workdir,
		BindWorkdir:           false,
		EventName:             j.eventName,
		EventPath:             cfg.EventPath,
		Platforms:             j.platforms,
		ReuseContainers:       false,
		ForceRebuild:          true,
		Env:                   cfg.Env,
		Secrets:               cfg.Secrets,
		Inputs:                cfg.Inputs,
		GitHubInstance:        "github.com",
		DefaultActionInstance: cfg.DefaultActionInstance,
		ContainerArchitecture: cfg.ContainerArchitecture,
		ContainerMaxLifetime:  time.Hour,
		Matrix:                cfg.Matrix,
		ActionCache:           cfg.ActionCache,
		ValidVolumes:          []string{"**"}, // allow workflow-declared volumes (e.g. container-volumes)
	}

	runner, err := New(runnerConfig)
	assert.NoError(t, err, j.workflowPath) //nolint:testifylint // pre-existing issue from nektos/act

	planner, err := model.NewWorkflowPlanner(fullWorkflowPath, true)
	assert.NoError(t, err, fullWorkflowPath) //nolint:testifylint // pre-existing issue from nektos/act

	plan, err := planner.PlanEvent(j.eventName)
	assert.True(t, (err == nil) != (plan == nil), "PlanEvent should return either a plan or an error") //nolint:testifylint // pre-existing issue from nektos/act
	if err == nil && plan != nil {
		err = runner.NewPlanExecutor(plan)(ctx)
		if j.errorMessage == "" {
			assert.NoError(t, err, fullWorkflowPath) //nolint:testifylint // pre-existing issue from nektos/act
		} else {
			assert.Error(t, err, j.errorMessage) //nolint:testifylint // pre-existing issue from nektos/act
		}
	}

	fmt.Println("::endgroup::") //nolint:forbidigo // pre-existing issue from nektos/act
}

type TestConfig struct {
	LocalRepositories map[string]string `yaml:"local-repositories"`
}

func TestRunEvent(t *testing.T) {
	requireDocker(t)

	ctx := context.Background()

	tables := []TestJobFileInfo{
		// Shells
		{workdir, "shells/defaults", "push", "", platforms, secrets},
		{workdir, "shells/bash", "push", "", platforms, secrets},
		{workdir, "shells/sh", "push", "", platforms, secrets},

		// Local action
		{workdir, "local-action-docker-url", "push", "", platforms, secrets},
		{workdir, "local-action-dockerfile", "push", "", platforms, secrets},
		{workdir, "local-action-via-composite-dockerfile", "push", "", platforms, secrets},
		{workdir, "local-action-js", "push", "", platforms, secrets},

		// Uses
		{workdir, "uses-composite", "push", "", platforms, secrets},
		{workdir, "uses-composite-with-error", "push", "Job 'failing-composite-action' failed", platforms, secrets},
		{workdir, "uses-docker-url", "push", "", platforms, secrets},
		{workdir, "act-composite-env-test", "push", "", platforms, secrets},

		// Eval
		{workdir, "evalmatrix", "push", "", platforms, secrets},
		{workdir, "evalmatrixneeds", "push", "", platforms, secrets},
		{workdir, "evalmatrixneeds2", "push", "", platforms, secrets},
		{workdir, "evalmatrix-merge-map", "push", "", platforms, secrets},
		{workdir, "evalmatrix-merge-array", "push", "", platforms, secrets},

		{workdir, "basic", "push", "", platforms, secrets},
		{workdir, "fail", "push", "exit with `FAILURE`: 1", platforms, secrets},
		{workdir, "checkout", "push", "", platforms, secrets},
		{workdir, "job-container", "push", "", platforms, secrets},
		{workdir, "job-container-non-root", "push", "", platforms, secrets},
		{workdir, "job-container-invalid-credentials", "push", "failed to handle credentials: failed to interpolate container.credentials.password", platforms, secrets},
		{workdir, "container-hostname", "push", "", platforms, secrets},
		{workdir, "matrix", "push", "", platforms, secrets},
		{workdir, "matrix-exitcode", "push", "Job 'test' failed", platforms, secrets},
		{workdir, "commands", "push", "", platforms, secrets},
		{workdir, "workdir", "push", "", platforms, secrets},
		{workdir, "defaults-run", "push", "", platforms, secrets},
		{workdir, "composite-fail-with-output", "push", "", platforms, secrets},
		{workdir, "issue-597", "push", "", platforms, secrets},
		{workdir, "issue-598", "push", "", platforms, secrets},
		{workdir, "if-env-act", "push", "", platforms, secrets},
		{workdir, "env-and-path", "push", "", platforms, secrets},
		{workdir, "environment-files", "push", "", platforms, secrets},
		{workdir, "GITHUB_STATE", "push", "", platforms, secrets},
		{workdir, "environment-files-parser-bug", "push", "", platforms, secrets},
		{workdir, "non-existent-action", "push", "Job 'nopanic' failed", platforms, secrets},
		{workdir, "outputs", "push", "", platforms, secrets},
		{workdir, "networking", "push", "", platforms, secrets},
		{workdir, "steps-context/conclusion", "push", "", platforms, secrets},
		{workdir, "steps-context/outcome", "push", "", platforms, secrets},
		{workdir, "job-status-check", "push", "job 'fail' failed", platforms, secrets},
		{workdir, "if-expressions", "push", "Job 'mytest' failed", platforms, secrets},
		{workdir, "actions-environment-and-context-tests", "push", "", platforms, secrets},
		{workdir, "evalenv", "push", "", platforms, secrets},
		{workdir, "docker-action-custom-path", "push", "", platforms, secrets},
		{workdir, "GITHUB_ENV-use-in-env-ctx", "push", "", platforms, secrets},
		{workdir, "ensure-post-steps", "push", "Job 'second-post-step-should-fail' failed", platforms, secrets},
		{workdir, "workflow_call_inputs", "workflow_call", "", platforms, secrets},
		{workdir, "workflow_dispatch", "workflow_dispatch", "", platforms, secrets},
		{workdir, "workflow_dispatch_no_inputs_mapping", "workflow_dispatch", "", platforms, secrets},
		{workdir, "workflow_dispatch-scalar", "workflow_dispatch", "", platforms, secrets},
		{workdir, "workflow_dispatch-scalar-composite-action", "workflow_dispatch", "", platforms, secrets},
		{workdir, "job-needs-context-contains-result", "push", "", platforms, secrets},
		{"../model/testdata", "container-volumes", "push", "", platforms, secrets},
		{workdir, "path-handling", "push", "", platforms, secrets},
		{workdir, "do-not-leak-step-env-in-composite", "push", "", platforms, secrets},
		{workdir, "set-env-step-env-override", "push", "", platforms, secrets},
		{workdir, "set-env-new-env-file-per-step", "push", "", platforms, secrets},
		{workdir, "no-panic-on-invalid-composite-action", "push", "jobs failed due to invalid action", platforms, secrets},

		// services
		{workdir, "services", "push", "", platforms, secrets},
		{workdir, "services-with-container", "push", "", platforms, secrets},

		// local remote action overrides
		{workdir, "local-remote-action-overrides", "push", "", platforms, secrets},
	}

	for _, table := range tables {
		t.Run(table.workflowPath, func(t *testing.T) {
			if table.workflowPath == "container-volumes" {
				// host /proc bind mounts are Linux-Docker-only
				requireLinuxDocker(t)
			}

			config := &Config{
				Secrets: table.secrets,
			}

			eventFile := filepath.Join(workdir, table.workflowPath, "event.json")
			if _, err := os.Stat(eventFile); err == nil {
				config.EventPath = eventFile
			}

			testConfigFile := filepath.Join(workdir, table.workflowPath, "config.yml")
			if file, err := os.ReadFile(testConfigFile); err == nil {
				testConfig := &TestConfig{}
				if yaml.Unmarshal(file, testConfig) == nil {
					if testConfig.LocalRepositories != nil {
						config.ActionCache = &LocalRepositoryCache{
							Parent: GoGitActionCache{
								path.Clean(path.Join(workdir, "cache")),
							},
							LocalRepositories: testConfig.LocalRepositories,
							CacheDirCache:     map[string]string{},
						}
					}
				}
			}

			table.runTest(ctx, t, config)
		})
	}
}

func TestRunEventHostEnvironment(t *testing.T) {
	// Runs steps directly on the host (the "-self-hosted" platform), so it needs the shells
	// and tools the workflows invoke. No network gate: every action these workflows reference
	// is a local `./` fixture or the skipped actions/checkout, so the suite runs offline (same
	// as TestRunEvent). Only the broadly-used interpreters are required up front; the pwsh- and
	// nix-specific cases gate on their own tool below so a missing pwsh/nix skips just those.
	requireHostTools(t, "bash", "node")

	ctx := context.Background()

	tables := []TestJobFileInfo{}

	if runtime.GOOS == "linux" {
		platforms := map[string]string{
			"ubuntu-latest": "-self-hosted",
		}

		tables = append(tables, []TestJobFileInfo{
			// Shells
			{workdir, "shells/defaults", "push", "", platforms, secrets},
			{workdir, "shells/pwsh", "push", "", platforms, secrets},
			{workdir, "shells/bash", "push", "", platforms, secrets},
			{workdir, "shells/sh", "push", "", platforms, secrets},

			// Local action
			{workdir, "local-action-js", "push", "", platforms, secrets},

			// Uses
			{workdir, "uses-composite", "push", "", platforms, secrets},
			{workdir, "uses-composite-with-error", "push", "Job 'failing-composite-action' failed", platforms, secrets},
			{workdir, "act-composite-env-test", "push", "", platforms, secrets},

			// Eval
			{workdir, "evalmatrix", "push", "", platforms, secrets},
			{workdir, "evalmatrixneeds", "push", "", platforms, secrets},
			{workdir, "evalmatrixneeds2", "push", "", platforms, secrets},
			{workdir, "evalmatrix-merge-map", "push", "", platforms, secrets},
			{workdir, "evalmatrix-merge-array", "push", "", platforms, secrets},

			{workdir, "fail", "push", "exit with `FAILURE`: 1", platforms, secrets},
			{workdir, "checkout", "push", "", platforms, secrets},
			{workdir, "matrix", "push", "", platforms, secrets},
			{workdir, "commands", "push", "", platforms, secrets},
			{workdir, "defaults-run", "push", "", platforms, secrets},
			{workdir, "composite-fail-with-output", "push", "", platforms, secrets},
			{workdir, "issue-597", "push", "", platforms, secrets},
			{workdir, "issue-598", "push", "", platforms, secrets},
			{workdir, "if-env-act", "push", "", platforms, secrets},
			{workdir, "env-and-path", "push", "", platforms, secrets},
			{workdir, "non-existent-action", "push", "Job 'nopanic' failed", platforms, secrets},
			{workdir, "outputs", "push", "", platforms, secrets},
			{workdir, "steps-context/conclusion", "push", "", platforms, secrets},
			{workdir, "steps-context/outcome", "push", "", platforms, secrets},
			{workdir, "job-status-check", "push", "job 'fail' failed", platforms, secrets},
			{workdir, "if-expressions", "push", "Job 'mytest' failed", platforms, secrets},
			{workdir, "evalenv", "push", "", platforms, secrets},
			{workdir, "ensure-post-steps", "push", "Job 'second-post-step-should-fail' failed", platforms, secrets},
		}...)
	}
	if runtime.GOOS == "windows" {
		platforms := map[string]string{
			"windows-latest": "-self-hosted",
		}

		tables = append(tables, []TestJobFileInfo{
			{workdir, "windows-prepend-path", "push", "", platforms, secrets},
			{workdir, "windows-add-env", "push", "", platforms, secrets},
			{workdir, "windows-shell-cmd", "push", "", platforms, secrets},
		}...)
	} else {
		platforms := map[string]string{
			"self-hosted":   "-self-hosted",
			"ubuntu-latest": "-self-hosted",
		}

		tables = append(tables, []TestJobFileInfo{
			{workdir, "nix-prepend-path", "push", "", platforms, secrets},
			{workdir, "inputs-via-env-context", "push", "", platforms, secrets},
			{workdir, "do-not-leak-step-env-in-composite", "push", "", platforms, secrets},
			{workdir, "set-env-step-env-override", "push", "", platforms, secrets},
			{workdir, "set-env-new-env-file-per-step", "push", "", platforms, secrets},
			{workdir, "no-panic-on-invalid-composite-action", "push", "jobs failed due to invalid action", platforms, secrets},
		}...)
	}

	for _, table := range tables {
		t.Run(table.workflowPath, func(t *testing.T) {
			switch table.workflowPath {
			case "shells/pwsh":
				requireHostTools(t, "pwsh")
			case "nix-prepend-path":
				requireHostTools(t, "nix")
			}
			table.runTest(ctx, t, &Config{})
		})
	}
}

func TestDryrunEvent(t *testing.T) {
	// Dryrun plans without containers or network (shells and local actions only).
	ctx := common.WithDryrun(context.Background(), true)

	tables := []TestJobFileInfo{
		// Shells
		{workdir, "shells/defaults", "push", "", platforms, secrets},
		{workdir, "shells/pwsh", "push", "", platforms, secrets},
		{workdir, "shells/bash", "push", "", platforms, secrets},
		{workdir, "shells/sh", "push", "", platforms, secrets},

		// Local action
		{workdir, "local-action-docker-url", "push", "", platforms, secrets},
		{workdir, "local-action-dockerfile", "push", "", platforms, secrets},
		{workdir, "local-action-via-composite-dockerfile", "push", "", platforms, secrets},
		{workdir, "local-action-js", "push", "", platforms, secrets},
	}

	for _, table := range tables {
		t.Run(table.workflowPath, func(t *testing.T) {
			table.runTest(ctx, t, &Config{})
		})
	}
}

// TestReusableWorkflowCaller exercises the reusable-workflow caller path against a local
// reusable workflow (typed inputs, secrets as both a map and `inherit`, and reading the called
// workflow's outputs via `needs`).
func TestReusableWorkflowCaller(t *testing.T) {
	requireDocker(t)
	table := TestJobFileInfo{workdir, "uses-workflow", "push", "", platforms, map[string]string{"secret": "keep_it_private"}}
	table.runTest(context.Background(), t, &Config{Secrets: table.secrets})
}

func TestDockerActionForcePullForceRebuild(t *testing.T) {
	requireDocker(t)
	requireNetwork(t) // force-pulls a docker action image

	ctx := context.Background()

	config := &Config{
		ForcePull:    true,
		ForceRebuild: true,
	}

	tables := []TestJobFileInfo{
		{workdir, "local-action-dockerfile", "push", "", platforms, secrets},
		{workdir, "local-action-via-composite-dockerfile", "push", "", platforms, secrets},
	}

	for _, table := range tables {
		t.Run(table.workflowPath, func(t *testing.T) {
			table.runTest(ctx, t, config)
		})
	}
}

type maskJobLoggerFactory struct {
	Output bytes.Buffer
}

func (f *maskJobLoggerFactory) WithJobLogger() *log.Logger {
	logger := log.New()
	logger.SetOutput(io.MultiWriter(&f.Output, os.Stdout))
	logger.SetLevel(log.DebugLevel)
	return logger
}

func TestMaskValues(t *testing.T) {
	assertNoSecret := func(text, secret string) { //nolint:unparam // pre-existing issue from nektos/act
		found := strings.Contains(text, "composite secret")
		if found {
			fmt.Printf("\nFound Secret in the given text:\n%s\n", text) //nolint:forbidigo // pre-existing issue from nektos/act
		}
		assert.False(t, strings.Contains(text, "composite secret")) //nolint:testifylint // pre-existing issue from nektos/act
	}

	requireDocker(t)

	log.SetLevel(log.DebugLevel)

	tjfi := TestJobFileInfo{
		workdir:      workdir,
		workflowPath: "mask-values",
		eventName:    "push",
		errorMessage: "",
		platforms:    platforms,
	}

	logger := &maskJobLoggerFactory{}
	tjfi.runTest(WithJobLoggerFactory(common.WithLogger(context.Background(), logger.WithJobLogger()), logger), t, &Config{})
	output := logger.Output.String()

	assertNoSecret(output, "secret value")
	assertNoSecret(output, "YWJjCg==")
}

func TestRunEventSecrets(t *testing.T) {
	requireDocker(t)
	workflowPath := "secrets"

	tjfi := TestJobFileInfo{
		workdir:      workdir,
		workflowPath: workflowPath,
		eventName:    "push",
		errorMessage: "",
		platforms:    platforms,
	}

	env, err := godotenv.Read(filepath.Join(workdir, workflowPath, ".env"))
	assert.NoError(t, err, "Failed to read .env") //nolint:testifylint // pre-existing issue from nektos/act
	secrets, _ := godotenv.Read(filepath.Join(workdir, workflowPath, ".secrets"))
	assert.NoError(t, err, "Failed to read .secrets") //nolint:testifylint // pre-existing issue from nektos/act

	tjfi.runTest(context.Background(), t, &Config{Secrets: secrets, Env: env})
}

func TestRunWithService(t *testing.T) {
	requireDocker(t)

	log.SetLevel(log.DebugLevel)
	ctx := context.Background()

	platforms := map[string]string{
		"ubuntu-latest": "node:24-bookworm-slim",
	}

	workflowPath := "services"
	eventName := "push"

	workdir, err := filepath.Abs("testdata")
	assert.NoError(t, err, workflowPath) //nolint:testifylint // pre-existing issue from nektos/act

	runnerConfig := &Config{
		Workdir:              workdir,
		EventName:            eventName,
		Platforms:            platforms,
		ReuseContainers:      false,
		ContainerMaxLifetime: time.Hour, // otherwise the job container is `sleep 0` and exits at once
	}
	runner, err := New(runnerConfig)
	assert.NoError(t, err, workflowPath) //nolint:testifylint // pre-existing issue from nektos/act

	planner, err := model.NewWorkflowPlanner("testdata/"+workflowPath, true)
	assert.NoError(t, err, workflowPath) //nolint:testifylint // pre-existing issue from nektos/act

	plan, err := planner.PlanEvent(eventName)
	assert.NoError(t, err, workflowPath) //nolint:testifylint // pre-existing issue from nektos/act

	err = runner.NewPlanExecutor(plan)(ctx)
	assert.NoError(t, err, workflowPath)
}

func TestRunActionInputs(t *testing.T) {
	requireDocker(t)
	workflowPath := "input-from-cli"

	tjfi := TestJobFileInfo{
		workdir:      workdir,
		workflowPath: workflowPath,
		eventName:    "workflow_dispatch",
		errorMessage: "",
		platforms:    platforms,
	}

	inputs := map[string]string{
		"SOME_INPUT": "input",
	}

	tjfi.runTest(context.Background(), t, &Config{Inputs: inputs})
}

func TestRunEventPullRequest(t *testing.T) {
	requireDocker(t)

	workflowPath := "pull-request"

	tjfi := TestJobFileInfo{
		workdir:      workdir,
		workflowPath: workflowPath,
		eventName:    "pull_request",
		errorMessage: "",
		platforms:    platforms,
	}

	tjfi.runTest(context.Background(), t, &Config{EventPath: filepath.Join(workdir, workflowPath, "event.json")})
}

func TestRunMatrixWithUserDefinedInclusions(t *testing.T) {
	requireDocker(t)
	workflowPath := "matrix-with-user-inclusions"

	tjfi := TestJobFileInfo{
		workdir:      workdir,
		workflowPath: workflowPath,
		eventName:    "push",
		errorMessage: "",
		platforms:    platforms,
	}

	matrix := map[string]map[string]bool{
		"node": {
			"8":   true,
			"8.x": true,
		},
		"os": {
			"ubuntu-18.04": true,
		},
	}

	tjfi.runTest(context.Background(), t, &Config{Matrix: matrix})
}
