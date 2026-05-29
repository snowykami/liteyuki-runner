// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitea.com/gitea/runner/act/artifactcache"
	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/model"
	"gitea.com/gitea/runner/act/runner"
	"gitea.com/gitea/runner/internal/pkg/client"
	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/labels"
	"gitea.com/gitea/runner/internal/pkg/metrics"
	"gitea.com/gitea/runner/internal/pkg/report"
	"gitea.com/gitea/runner/internal/pkg/ver"

	runnerv1 "code.gitea.io/actions-proto-go/runner/v1"
	"connectrpc.com/connect"
	"github.com/moby/moby/api/types/container"
	log "github.com/sirupsen/logrus"
)

// Runner runs the pipeline.
type Runner struct {
	name string

	cfg *config.Config

	client       client.Client
	labels       labels.Labels
	envs         map[string]string
	cacheHandler *artifactcache.Handler

	runningTasks            sync.Map
	runningCount            atomic.Int64
	lastIdleCleanupUnixNano atomic.Int64
	now                     func() time.Time
}

func NewRunner(cfg *config.Config, reg *config.Registration, cli client.Client) *Runner {
	ls := labels.Labels{}
	for _, v := range reg.Labels {
		if l, err := labels.Parse(v); err == nil {
			ls = append(ls, l)
		}
	}
	envs := make(map[string]string, len(cfg.Runner.Envs))
	maps.Copy(envs, cfg.Runner.Envs)
	var cacheHandler *artifactcache.Handler
	if cfg.Cache.Enabled == nil || *cfg.Cache.Enabled {
		if cfg.Cache.ExternalServer != "" {
			envs["ACTIONS_CACHE_URL"] = cfg.Cache.ExternalServer
		} else {
			handler, err := artifactcache.StartHandler(
				cfg.Cache.Dir,
				cfg.Cache.Host,
				cfg.Cache.Port,
				"",
				log.StandardLogger().WithField("module", "cache_request"),
			)
			if err != nil {
				log.Errorf("cannot init cache server, it will be disabled: %v", err)
				// go on
			} else {
				cacheHandler = handler
				envs["ACTIONS_CACHE_URL"] = handler.ExternalURL() + "/"
			}
		}
	}

	// set artifact gitea api
	artifactGiteaAPI := strings.TrimSuffix(cli.Address(), "/") + "/api/actions_pipeline/"
	envs["ACTIONS_RUNTIME_URL"] = artifactGiteaAPI
	envs["ACTIONS_RESULTS_URL"] = strings.TrimSuffix(cli.Address(), "/")

	// Set specific environments to distinguish between Gitea and GitHub
	envs["GITEA_ACTIONS"] = "true"
	envs["GITEA_ACTIONS_RUNNER_VERSION"] = ver.Version()

	runner := &Runner{
		name:         reg.Name,
		cfg:          cfg,
		client:       cli,
		labels:       ls,
		envs:         envs,
		cacheHandler: cacheHandler,
		now:          time.Now,
	}
	return runner
}

// OnIdle performs lightweight maintenance during polling idle windows.
// It runs synchronously on the poller goroutine; shouldRunIdleCleanup
// throttles invocations to runner.idle_cleanup_interval so the impact on
// poll cadence is bounded even when the workdir root is large.
func (r *Runner) OnIdle(ctx context.Context) {
	if !r.shouldRunIdleCleanup() {
		return
	}
	workdirParent := strings.TrimLeft(r.cfg.Container.WorkdirParent, "/")
	workdirRoot := filepath.FromSlash("/" + workdirParent)
	r.cleanupStaleTaskDirs(ctx, workdirRoot)
}

func (r *Runner) shouldRunIdleCleanup() bool {
	if !r.cfg.Container.BindWorkdir {
		return false
	}
	if r.cfg.Runner.WorkdirCleanupAge <= 0 || r.cfg.Runner.IdleCleanupInterval <= 0 {
		return false
	}
	if r.RunningCount() != 0 {
		return false
	}
	now := r.now()
	interval := r.cfg.Runner.IdleCleanupInterval
	for {
		last := r.lastIdleCleanupUnixNano.Load()
		if last != 0 && now.Sub(time.Unix(0, last)) < interval {
			return false
		}
		if r.lastIdleCleanupUnixNano.CompareAndSwap(last, now.UnixNano()) {
			return true
		}
	}
}

func (r *Runner) cleanupStaleTaskDirs(ctx context.Context, workdirRoot string) {
	entries, err := os.ReadDir(workdirRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		log.Warnf("failed to list task workspace root %s for stale cleanup: %v", workdirRoot, err)
		return
	}

	// A task may begin between shouldRunIdleCleanup's running-count check and
	// the loop below. That is safe because new task dirs are created with the
	// current mtime and therefore fall on the keep side of cutoff.
	cutoff := r.now().Add(-r.cfg.Runner.WorkdirCleanupAge)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return
		}
		if !entry.IsDir() {
			continue
		}
		// Task workspaces are indexed by numeric task IDs; skip any other
		// directories to avoid deleting operator-managed data under workdir_root.
		if _, err := strconv.ParseUint(entry.Name(), 10, 64); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			log.Warnf("failed to stat task workspace %s: %v", filepath.Join(workdirRoot, entry.Name()), err)
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		taskDir := filepath.Join(workdirRoot, entry.Name())
		if err := os.RemoveAll(taskDir); err != nil {
			log.Warnf("failed to clean stale task workspace %s: %v", taskDir, err)
			continue
		}
		log.Infof("cleaned stale task workspace %s", taskDir)
	}
}

func (r *Runner) Run(ctx context.Context, task *runnerv1.Task) error {
	if _, ok := r.runningTasks.Load(task.Id); ok {
		return fmt.Errorf("task %d is already running", task.Id)
	}
	r.runningTasks.Store(task.Id, struct{}{})
	defer r.runningTasks.Delete(task.Id)

	r.runningCount.Add(1)

	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, r.cfg.Runner.Timeout)
	defer cancel()
	reporter := report.NewReporter(ctx, cancel, r.client, task, r.cfg)
	var runErr error
	defer func() {
		r.runningCount.Add(-1)

		lastWords := ""
		if runErr != nil {
			lastWords = runErr.Error()
		}
		_ = reporter.Close(lastWords)

		metrics.JobDuration.Observe(time.Since(start).Seconds())
		metrics.JobsTotal.WithLabelValues(metrics.ResultToStatusLabel(reporter.Result())).Inc()
	}()
	reporter.RunDaemon()
	runErr = r.run(ctx, task, reporter)

	return nil
}

func (r *Runner) cloneEnvs() map[string]string {
	// +3 reserves space for the per-task keys injected by run():
	// ACTIONS_ID_TOKEN_REQUEST_URL, ACTIONS_ID_TOKEN_REQUEST_TOKEN, ACTIONS_RUNTIME_TOKEN.
	envs := make(map[string]string, len(r.envs)+3)
	maps.Copy(envs, r.envs)
	return envs
}

// getDefaultActionsURL
// when DEFAULT_ACTIONS_URL == "https://github.com" and GithubMirror is not blank,
// it should be set to GithubMirror first.
func (r *Runner) getDefaultActionsURL(task *runnerv1.Task) string {
	giteaDefaultActionsURL := task.Context.Fields["gitea_default_actions_url"].GetStringValue()
	if giteaDefaultActionsURL == "https://github.com" && r.cfg.Runner.GithubMirror != "" {
		return r.cfg.Runner.GithubMirror
	}
	return giteaDefaultActionsURL
}

func (r *Runner) run(ctx context.Context, task *runnerv1.Task, reporter *report.Reporter) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	repo := task.Context.Fields["repository"].GetStringValue()
	if !r.canRunRepo(repo) {
		rejectText := strings.ReplaceAll(r.cfg.Runner.RejectText, "{REPO}", repo)
		rejectText = strings.ReplaceAll(rejectText, "{RUNNER}", r.name)
		log.Warnf("%s", rejectText)
		reporter.Logf("%s", rejectText)
		return errors.New("repository not matched allowed_repos")
	}

	reporter.Logf("%s(version:%s) received task %v of job %v, be triggered by event: %s", r.name, ver.Version(), task.Id, task.Context.Fields["job"].GetStringValue(), task.Context.Fields["event_name"].GetStringValue())

	workflow, jobID, err := generateWorkflow(task)
	if err != nil {
		return err
	}

	plan, err := model.CombineWorkflowPlanner(workflow).PlanJob(jobID)
	if err != nil {
		return err
	}
	job := workflow.GetJob(jobID)
	reporter.ResetSteps(len(job.Steps))

	taskContext := task.Context.Fields
	envs := r.cloneEnvs()

	log.Infof("task %v repo is %v %v %v", task.Id, taskContext["repository"].GetStringValue(),
		r.getDefaultActionsURL(task),
		r.client.Address())

	preset := &model.GithubContext{
		Event:           taskContext["event"].GetStructValue().AsMap(),
		RunID:           taskContext["run_id"].GetStringValue(),
		RunNumber:       taskContext["run_number"].GetStringValue(),
		RunAttempt:      taskContext["run_attempt"].GetStringValue(),
		Actor:           taskContext["actor"].GetStringValue(),
		Repository:      taskContext["repository"].GetStringValue(),
		EventName:       taskContext["event_name"].GetStringValue(),
		Sha:             taskContext["sha"].GetStringValue(),
		Ref:             taskContext["ref"].GetStringValue(),
		RefName:         taskContext["ref_name"].GetStringValue(),
		RefType:         taskContext["ref_type"].GetStringValue(),
		HeadRef:         taskContext["head_ref"].GetStringValue(),
		BaseRef:         taskContext["base_ref"].GetStringValue(),
		Token:           taskContext["token"].GetStringValue(),
		RepositoryOwner: taskContext["repository_owner"].GetStringValue(),
		RetentionDays:   taskContext["retention_days"].GetStringValue(),
	}
	if t := task.Secrets["GITEA_TOKEN"]; t != "" {
		preset.Token = t
	} else if t := task.Secrets["GITHUB_TOKEN"]; t != "" {
		preset.Token = t
	}

	if actionsIDTokenRequestURL := taskContext["actions_id_token_request_url"].GetStringValue(); actionsIDTokenRequestURL != "" {
		envs["ACTIONS_ID_TOKEN_REQUEST_URL"] = actionsIDTokenRequestURL
		envs["ACTIONS_ID_TOKEN_REQUEST_TOKEN"] = taskContext["actions_id_token_request_token"].GetStringValue()
		task.Secrets["ACTIONS_ID_TOKEN_REQUEST_TOKEN"] = envs["ACTIONS_ID_TOKEN_REQUEST_TOKEN"]
	}

	giteaRuntimeToken := taskContext["gitea_runtime_token"].GetStringValue()
	if giteaRuntimeToken == "" {
		// use task token to action api token for previous Gitea Server Versions
		giteaRuntimeToken = preset.Token
	}
	envs["ACTIONS_RUNTIME_TOKEN"] = giteaRuntimeToken
	// Mask the runtime token so it cannot be echoed in user step output; it is
	// now also the cache server's bearer credential and leaking it would let
	// any reader of the log impersonate this job against the cache.
	if giteaRuntimeToken != "" {
		task.Secrets["ACTIONS_RUNTIME_TOKEN"] = giteaRuntimeToken
	}

	// Register this job's runtime token with the local cache server so that
	// cache requests from the job container can authenticate. The credential
	// is removed when the task finishes, so a leaked token stops working as
	// soon as the job ends rather than remaining valid for the runner's
	// lifetime. Only applies to the embedded cache server; when the operator
	// points the runner at an external cache via cfg.Cache.ExternalServer, it
	// is that server's responsibility to authenticate requests.
	defer r.registerCacheForTask(giteaRuntimeToken, preset.Repository, reporter)()

	eventJSON, err := json.Marshal(preset.Event)
	if err != nil {
		return err
	}

	maxLifetime := 3 * time.Hour
	if deadline, ok := ctx.Deadline(); ok {
		maxLifetime = time.Until(deadline)
	}

	workdirParent := strings.TrimLeft(r.cfg.Container.WorkdirParent, "/")
	if r.cfg.Container.BindWorkdir {
		// Append the task ID to isolate concurrent jobs from the same repo.
		workdirParent = fmt.Sprintf("%s/%d", workdirParent, task.Id)
	}
	workdir := filepath.FromSlash(fmt.Sprintf("/%s/%s", workdirParent, preset.Repository))
	if runtime.GOOS == "windows" {
		if abs, err := filepath.Abs(workdir); err == nil {
			workdir = abs
		}
	}
	// Without bind_workdir, the workspace path omits the task id; concurrent host-mode jobs
	// for the same repository would share this directory and can race with per-job cleanup.

	runnerConfig := &runner.Config{
		// On Linux, Workdir will be like "/<parent_directory>/<owner>/<repo>"
		// On Windows, Workdir will be like "\<parent_directory>\<owner>\<repo>"
		Workdir:           workdir,
		BindWorkdir:       r.cfg.Container.BindWorkdir,
		ActionCacheDir:    filepath.FromSlash(r.cfg.Host.WorkdirParent),
		AllocatePTY:       r.cfg.Runner.AllocatePTY,
		ActionOfflineMode: r.cfg.Cache.OfflineMode,

		ReuseContainers:       false,
		ForcePull:             r.cfg.Container.ForcePull,
		ForceRebuild:          r.cfg.Container.ForceRebuild,
		LogOutput:             true,
		JSONLogger:            false,
		Env:                   envs,
		Secrets:               task.Secrets,
		GitHubInstance:        strings.TrimSuffix(r.client.Address(), "/"),
		AutoRemove:            true,
		NoSkipCheckout:        true,
		PresetGitHubContext:   preset,
		EventJSON:             string(eventJSON),
		ContainerNamePrefix:   fmt.Sprintf("GITEA-ACTIONS-TASK-%d", task.Id),
		ContainerMaxLifetime:  maxLifetime,
		CleanWorkdir:          true,
		ContainerNetworkMode:  container.NetworkMode(r.cfg.Container.Network),
		ContainerOptions:      r.cfg.Container.Options,
		ContainerDaemonSocket: r.cfg.Container.DockerHost,
		Privileged:            r.cfg.Container.Privileged,
		DefaultActionInstance: r.getDefaultActionsURL(task),
		PlatformPicker:        r.labels.PickPlatform,
		Vars:                  task.Vars,
		ValidVolumes:          r.cfg.Container.ValidVolumes,
		InsecureSkipTLS:       r.cfg.Runner.Insecure,
	}

	rr, err := runner.New(runnerConfig)
	if err != nil {
		return err
	}
	executor := rr.NewPlanExecutor(plan)

	reporter.Logf("workflow prepared")

	// add logger recorders
	ctx = common.WithLoggerHook(ctx, reporter)

	if !log.IsLevelEnabled(log.DebugLevel) {
		ctx = runner.WithJobLoggerFactory(ctx, NullLogger{})
	}

	execErr := executor(ctx)
	reporter.SetOutputs(job.Outputs)

	if r.cfg.Container.BindWorkdir {
		// Remove the entire task-specific directory (e.g. /workspace/<task_id>).
		taskDir := filepath.FromSlash("/" + workdirParent)
		if err := os.RemoveAll(taskDir); err != nil {
			log.Warnf("failed to clean up workspace %s: %v", taskDir, err)
		}
	}

	return execErr
}

// registerCacheForTask tells the cache server to accept requests authenticated
// with the given runtime token for the duration of this task. Returns a
// function the caller must invoke (typically via defer) to revoke the
// credential when the task finishes.
//
// Three modes:
//   - Embedded handler: register in-process via RegisterJob.
//   - external_server + external_secret: POST to the remote server's
//     /_internal/register, defer a POST to /_internal/revoke. This is what
//     enables full per-job auth and repo scoping over the network.
//   - external_server alone (no secret): no-op revoker. The remote server is
//     in legacy openMode and ignores the runtime token; trust is at the
//     network layer.
//
// Safe with an empty token (older Gitea did not issue one).
func (r *Runner) registerCacheForTask(token, repo string, reporter *report.Reporter) func() {
	if token == "" {
		return func() {}
	}
	if r.cacheHandler != nil {
		return r.cacheHandler.RegisterJob(token, repo)
	}
	if r.cfg.Cache.ExternalServer != "" && r.cfg.Cache.ExternalSecret != "" {
		return r.registerExternalCacheJob(token, repo, reporter)
	}
	return func() {}
}

// registerExternalCacheJob POSTs to the remote cache-server's control-plane.
// Failures are logged but not fatal: if registration fails, the cache will
// 401 the job's requests — better than failing the whole task for a cache
// outage. The warning is mirrored to the job log so users can see why their
// cache calls 401, instead of having to read the runner daemon's stderr.
func (r *Runner) registerExternalCacheJob(token, repo string, reporter *report.Reporter) func() {
	base := strings.TrimRight(r.cfg.Cache.ExternalServer, "/")
	if err := postInternalCache(base+"/_internal/register", r.cfg.Cache.ExternalSecret,
		map[string]string{"token": token, "repo": repo}); err != nil {
		log.Warnf("cache external_server register failed (%s): %v", base, err)
		if reporter != nil {
			reporter.Logf("::warning::cache external_server register failed (%s): %v — cache requests from this job will be unauthenticated and likely return 401", base, err)
		}
	}
	return func() {
		if err := postInternalCache(base+"/_internal/revoke", r.cfg.Cache.ExternalSecret,
			map[string]string{"token": token}); err != nil {
			log.Warnf("cache external_server revoke failed (%s): %v", base, err)
			if reporter != nil {
				reporter.Logf("::warning::cache external_server revoke failed (%s): %v", base, err)
			}
		}
	}
}

func postInternalCache(url, secret string, body map[string]string) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func (r *Runner) RunningCount() int64 {
	return r.runningCount.Load()
}

func (r *Runner) Declare(ctx context.Context, labels []string) (*connect.Response[runnerv1.DeclareResponse], error) {
	return r.client.Declare(ctx, connect.NewRequest(&runnerv1.DeclareRequest{
		Version: ver.Version(),
		Labels:  labels,
	}))
}

func (r *Runner) canRunRepo(repo string) bool {
	matched := matchAllowedRepo(repo, r.cfg.Runner.AllowedRepos)
	if r.cfg.Runner.BlacklistMode {
		return !matched
	}
	return matched
}

func matchAllowedRepo(targetRepo string, allowedRepos []string) bool {
	if len(allowedRepos) == 0 {
		return true
	}

	targetOwner, targetRepoName, ok := splitRepo(targetRepo)
	if !ok {
		log.Errorf("invalid repository format: %s", targetRepo)
		return false
	}

	for _, allowedRepo := range allowedRepos {
		allowedOwner, allowedRepoName, ok := splitRepo(allowedRepo)
		if !ok {
			log.Warnf("invalid allowed repository format: %s", allowedRepo)
			continue
		}
		if matchRepoPart(allowedOwner, targetOwner) && matchRepoPart(allowedRepoName, targetRepoName) {
			return true
		}
	}
	return false
}

func splitRepo(repo string) (string, string, bool) {
	owner, name, ok := strings.Cut(repo, "/")
	return owner, name, ok && owner != "" && name != "" && !strings.Contains(name, "/")
}

func matchRepoPart(pattern, value string) bool {
	return pattern == "*" || strings.EqualFold(pattern, value)
}
