// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	maps0 "maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/container"
	"gitea.com/gitea/runner/act/exprparser"
	"gitea.com/gitea/runner/act/model"

	"github.com/docker/go-connections/nat"
	"github.com/opencontainers/selinux/go-selinux"
)

// RunContext contains info about current job
type RunContext struct {
	Name                string
	Config              *Config
	Matrix              map[string]any
	Run                 *model.Run
	EventJSON           string
	Env                 map[string]string
	GlobalEnv           map[string]string // to pass env changes of GITHUB_ENV and set-env correctly, due to dirty Env field
	ExtraPath           []string
	CurrentStep         string
	StepResults         map[string]*model.StepResult
	IntraActionState    map[string]map[string]string
	ExprEval            ExpressionEvaluator
	JobContainer        container.ExecutionsEnvironment
	ServiceContainers   []container.ExecutionsEnvironment
	OutputMappings      map[MappableOutput]MappableOutput
	JobName             string
	ActionPath          string
	Parent              *RunContext
	Masks               []string
	cleanUpJobContainer common.Executor
	caller              *caller // job calling this RunContext (reusable workflows)
	// outputTemplate is this combination's pristine snapshot of the job's output expressions,
	// captured before execution so each matrix combo interpolates from the originals rather
	// than from a sibling's already-resolved values written into the shared Job.Outputs.
	outputTemplate map[string]string
}

func (rc *RunContext) AddMask(mask string) {
	rc.Masks = append(rc.Masks, mask)
}

type MappableOutput struct {
	StepID     string
	OutputName string
}

func (rc *RunContext) String() string {
	name := fmt.Sprintf("%s/%s", rc.Run.Workflow.Name, rc.Name)
	if rc.caller != nil {
		// prefix the reusable workflow with the caller job
		// this is required to create unique container names
		name = fmt.Sprintf("%s/%s", rc.caller.runContext.Name, name)
	}
	return name
}

// GetEnv returns the env for the context
func (rc *RunContext) GetEnv() map[string]string {
	if rc.Env == nil {
		rc.Env = map[string]string{}
		if rc.Run != nil && rc.Run.Workflow != nil && rc.Config != nil {
			job := rc.Run.Job()
			if job != nil {
				rc.Env = mergeMaps(rc.Run.Workflow.Env, job.Environment(), rc.Config.Env)
			}
		}
	}
	rc.Env["ACT"] = "true"

	if !rc.Config.NoSkipCheckout {
		rc.Env["ACT_SKIP_CHECKOUT"] = "true"
	}

	return rc.Env
}

func (rc *RunContext) jobContainerName() string {
	nameParts := []string{rc.Config.ContainerNamePrefix, "WORKFLOW-" + rc.Run.Workflow.Name, "JOB-" + rc.Name}
	if rc.caller != nil {
		nameParts = append(nameParts, "CALLED-BY-"+rc.caller.runContext.JobName)
	}
	return createContainerName(nameParts...) // For Gitea
}

// networkNameForGitea return the name of the network
func (rc *RunContext) networkNameForGitea() (string, bool) {
	if rc.Config.ContainerNetworkMode != "" {
		return string(rc.Config.ContainerNetworkMode), false
	}
	return fmt.Sprintf("%s-%s-network", rc.jobContainerName(), rc.Run.JobID), true
}

func getDockerDaemonSocketMountPath(daemonPath string) string {
	if before, after, ok := strings.Cut(daemonPath, "://"); ok {
		scheme := before
		if strings.EqualFold(scheme, "npipe") {
			// linux container mount on windows, use the default socket path of the VM / wsl2
			return "/var/run/docker.sock"
		} else if strings.EqualFold(scheme, "unix") {
			return after
		} else if strings.IndexFunc(scheme, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z')
		}) == -1 {
			// unknown protocol use default
			return "/var/run/docker.sock"
		}
	}
	return daemonPath
}

// containerDaemonSocket returns the configured Docker daemon socket, applying the default
// without mutating the shared Config. Parallel jobs in a plan share one *Config, so a job
// must never write to it.
func (rc *RunContext) containerDaemonSocket() string {
	if rc.Config.ContainerDaemonSocket == "" {
		return "/var/run/docker.sock"
	}
	return rc.Config.ContainerDaemonSocket
}

// validVolumes returns the volumes allowed on this job's containers: the configured base
// plus the volumes the runner mounts automatically. It derives a fresh slice every call and
// never mutates the shared Config (see containerDaemonSocket).
func (rc *RunContext) validVolumes() []string {
	name := rc.jobContainerName()
	volumes := slices.Clone(rc.Config.ValidVolumes)
	// TODO: add a new configuration to control whether the docker daemon can be mounted
	return append(volumes, "act-toolcache", name, name+"-env",
		getDockerDaemonSocketMountPath(rc.containerDaemonSocket()))
}

// Returns the binds and mounts for the container, resolving paths as appopriate
func (rc *RunContext) GetBindsAndMounts() ([]string, map[string]string) {
	name := rc.jobContainerName()

	binds := []string{}
	if daemonSocket := rc.containerDaemonSocket(); daemonSocket != "-" {
		daemonPath := getDockerDaemonSocketMountPath(daemonSocket)
		binds = append(binds, fmt.Sprintf("%s:%s", daemonPath, "/var/run/docker.sock"))
	}

	ext := container.LinuxContainerEnvironmentExtensions{}

	mounts := map[string]string{
		"act-toolcache": "/opt/hostedtoolcache",
		name + "-env":   ext.GetActPath(),
	}

	if job := rc.Run.Job(); job != nil {
		if container := job.Container(); container != nil {
			for _, v := range container.Volumes {
				if !strings.Contains(v, ":") || filepath.IsAbs(v) {
					// Bind anonymous volume or host file.
					binds = append(binds, v)
				} else {
					// Mount existing volume.
					paths := strings.SplitN(v, ":", 2)
					mounts[paths[0]] = paths[1]
				}
			}
		}
	}

	if rc.Config.BindWorkdir {
		bindModifiers := ""
		if runtime.GOOS == "darwin" {
			bindModifiers = ":delegated"
		}
		if selinux.GetEnabled() {
			bindModifiers = ":z"
		}
		binds = append(binds, fmt.Sprintf("%s:%s%s", rc.Config.Workdir, ext.ToContainerPath(rc.Config.Workdir), bindModifiers))
	} else {
		mounts[name] = ext.ToContainerPath(rc.Config.Workdir)
	}

	return binds, mounts
}

func (rc *RunContext) startHostEnvironment() common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		rawLogger := logger.WithField(rawOutputField, true)
		logWriter := common.NewLineWriter(rc.commandHandler(ctx), func(s string) bool {
			if rc.Config.LogOutput {
				rawLogger.Infof("%s", s)
			} else {
				rawLogger.Debugf("%s", s)
			}
			return true
		})
		cacheDir := rc.ActionCacheDir()
		randBytes := make([]byte, 8)
		_, _ = rand.Read(randBytes)
		miscpath := filepath.Join(cacheDir, hex.EncodeToString(randBytes))
		actPath := filepath.Join(miscpath, "act")
		if err := os.MkdirAll(actPath, 0o777); err != nil {
			return err
		}
		path := filepath.Join(miscpath, "hostexecutor")
		if err := os.MkdirAll(path, 0o777); err != nil {
			return err
		}
		runnerTmp := filepath.Join(miscpath, "tmp")
		if err := os.MkdirAll(runnerTmp, 0o777); err != nil {
			return err
		}
		toolCache := filepath.Join(cacheDir, "tool_cache")
		rc.JobContainer = &container.HostEnvironment{
			Path:         path,
			TmpDir:       runnerTmp,
			ToolCache:    toolCache,
			Workdir:      rc.Config.Workdir,
			CleanWorkdir: rc.Config.CleanWorkdir,
			ActPath:      actPath,
			CleanUp: func() {
				os.RemoveAll(miscpath)
			},
			StdOut:      logWriter,
			AllocatePTY: rc.Config.AllocatePTY,
		}
		rc.cleanUpJobContainer = rc.JobContainer.Remove()
		for k, v := range rc.JobContainer.GetRunnerContext(ctx) {
			if v, ok := v.(string); ok {
				rc.Env["RUNNER_"+strings.ToUpper(k)] = v
			}
		}
		for _, env := range os.Environ() {
			if k, v, ok := strings.Cut(env, "="); ok {
				// don't override
				if _, ok := rc.Env[k]; !ok {
					rc.Env[k] = v
				}
			}
		}

		return common.NewPipelineExecutor(
			rc.JobContainer.Copy(rc.JobContainer.GetActPath()+"/", &container.FileEntry{
				Name: "workflow/event.json",
				Mode: 0o644,
				Body: rc.EventJSON,
			}, &container.FileEntry{
				Name: "workflow/envs.txt",
				Mode: 0o666,
				Body: "",
			}),
		)(ctx)
	}
}

// printStartJobContainerGroup mirrors actions/runner's "Starting job container"
// section: emit the group header and summary, return a closer for ::endgroup::.
func printStartJobContainerGroup(ctx context.Context, image, name, network string) func() {
	rawLogger := common.Logger(ctx).WithField(rawOutputField, true)
	rawLogger.Infof("::group::Starting job container")
	rawLogger.Infof("image: %s", image)
	rawLogger.Infof("name: %s", name)
	rawLogger.Infof("network: %s", network)
	return func() {
		rawLogger.Infof("::endgroup::")
	}
}

func (rc *RunContext) startJobContainer() common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		image := rc.platformImage(ctx)
		rawLogger := logger.WithField(rawOutputField, true)
		logWriter := common.NewLineWriter(rc.commandHandler(ctx), func(s string) bool {
			if rc.Config.LogOutput {
				rawLogger.Infof("%s", s)
			} else {
				rawLogger.Debugf("%s", s)
			}
			return true
		})

		username, password, err := rc.handleCredentials(ctx)
		if err != nil {
			return fmt.Errorf("failed to handle credentials: %s", err)
		}

		name := rc.jobContainerName()
		// For gitea, to support --volumes-from <container_name_or_id> in options.
		// We need to set the container name to the environment variable.
		rc.Env["JOB_CONTAINER_NAME"] = name

		envList := make([]string, 0)

		envList = append(envList, fmt.Sprintf("%s=%s", "RUNNER_TOOL_CACHE", "/opt/hostedtoolcache"))
		envList = append(envList, fmt.Sprintf("%s=%s", "RUNNER_OS", "Linux"))
		envList = append(envList, fmt.Sprintf("%s=%s", "RUNNER_ARCH", container.RunnerArch(ctx)))
		envList = append(envList, fmt.Sprintf("%s=%s", "RUNNER_TEMP", "/tmp"))
		envList = append(envList, fmt.Sprintf("%s=%s", "LANG", "C.UTF-8")) // Use same locale as GitHub Actions

		ext := container.LinuxContainerEnvironmentExtensions{}
		binds, mounts := rc.GetBindsAndMounts()

		// specify the network to which the container will connect when `docker create` stage. (like execute command line: docker create --network <networkName> <image>)
		// if using service containers, will create a new network for the containers.
		// and it will be removed after at last.
		networkName, createAndDeleteNetwork := rc.networkNameForGitea()

		// add service containers
		for serviceID, spec := range rc.Run.Job().Services {
			// interpolate env
			interpolatedEnvs := make(map[string]string, len(spec.Env))
			for k, v := range spec.Env {
				interpolatedEnvs[k] = rc.ExprEval.Interpolate(ctx, v)
			}
			envs := make([]string, 0, len(interpolatedEnvs))
			for k, v := range interpolatedEnvs {
				envs = append(envs, fmt.Sprintf("%s=%s", k, v))
			}
			// interpolate cmd
			interpolatedCmd := make([]string, 0, len(spec.Cmd))
			for _, v := range spec.Cmd {
				interpolatedCmd = append(interpolatedCmd, rc.ExprEval.Interpolate(ctx, v))
			}
			username, password, err = rc.handleServiceCredentials(ctx, spec.Credentials)
			if err != nil {
				return fmt.Errorf("failed to handle service %s credentials: %w", serviceID, err)
			}

			interpolatedVolumes := make([]string, 0, len(spec.Volumes))
			for _, volume := range spec.Volumes {
				interpolatedVolumes = append(interpolatedVolumes, rc.ExprEval.Interpolate(ctx, volume))
			}
			serviceBinds, serviceMounts := rc.GetServiceBindsAndMounts(interpolatedVolumes)

			interpolatedPorts := make([]string, 0, len(spec.Ports))
			for _, port := range spec.Ports {
				interpolatedPorts = append(interpolatedPorts, rc.ExprEval.Interpolate(ctx, port))
			}
			exposedPorts, portBindings, err := nat.ParsePortSpecs(interpolatedPorts)
			if err != nil {
				return fmt.Errorf("failed to parse service %s ports: %w", serviceID, err)
			}

			serviceContainerName := createContainerName(rc.jobContainerName(), serviceID)
			c := container.NewContainer(&container.NewContainerInput{
				Name:           serviceContainerName,
				WorkingDir:     ext.ToContainerPath(rc.Config.Workdir),
				Image:          rc.ExprEval.Interpolate(ctx, spec.Image),
				Username:       username,
				Password:       password,
				Cmd:            interpolatedCmd,
				Env:            envs,
				Mounts:         serviceMounts,
				Binds:          serviceBinds,
				Stdout:         logWriter,
				Stderr:         logWriter,
				Privileged:     rc.Config.Privileged,
				UsernsMode:     rc.Config.UsernsMode,
				Platform:       rc.Config.ContainerArchitecture,
				AutoRemove:     rc.Config.AutoRemove,
				Options:        rc.ExprEval.Interpolate(ctx, spec.Options),
				NetworkMode:    networkName,
				NetworkAliases: []string{serviceID},
				ExposedPorts:   exposedPorts,
				PortBindings:   portBindings,
				AllocatePTY:    rc.Config.AllocatePTY,
			})
			rc.ServiceContainers = append(rc.ServiceContainers, c)
		}

		rc.cleanUpJobContainer = func(ctx context.Context) error {
			reuseJobContainer := func(ctx context.Context) bool {
				return rc.Config.ReuseContainers
			}

			if rc.JobContainer != nil {
				return rc.JobContainer.Remove().IfNot(reuseJobContainer).
					Then(container.NewDockerVolumeRemoveExecutor(rc.jobContainerName(), false)).IfNot(reuseJobContainer).
					Then(container.NewDockerVolumeRemoveExecutor(rc.jobContainerName()+"-env", false)).IfNot(reuseJobContainer).
					Then(func(ctx context.Context) error {
						if len(rc.ServiceContainers) > 0 {
							logger.Infof("Cleaning up services for job %s", rc.JobName)
							if err := rc.stopServiceContainers()(ctx); err != nil {
								logger.Errorf("Error while cleaning services: %v", err)
							}
						}
						if createAndDeleteNetwork {
							// clean network if it has been created by act
							// if using service containers
							// it means that the network to which containers are connecting is created by `runner`,
							// so, we should remove the network at last.
							logger.Infof("Cleaning up network for job %s, and network name is: %s", rc.JobName, networkName)
							if err := container.NewDockerNetworkRemoveExecutor(networkName)(ctx); err != nil {
								logger.Errorf("Error while cleaning network: %v", err)
							}
						}
						return nil
					})(ctx)
			}
			return nil
		}

		// For Gitea, `jobContainerNetwork` should be the same as `networkName`
		jobContainerNetwork := networkName

		rc.JobContainer = container.NewContainer(&container.NewContainerInput{
			Cmd:            nil,
			Entrypoint:     []string{"/bin/sleep", fmt.Sprint(rc.Config.ContainerMaxLifetime.Round(time.Second).Seconds())},
			WorkingDir:     ext.ToContainerPath(rc.Config.Workdir),
			Image:          image,
			Username:       username,
			Password:       password,
			Name:           name,
			Env:            envList,
			Mounts:         mounts,
			NetworkMode:    jobContainerNetwork,
			NetworkAliases: []string{rc.Name},
			Binds:          binds,
			Stdout:         logWriter,
			Stderr:         logWriter,
			Privileged:     rc.Config.Privileged,
			UsernsMode:     rc.Config.UsernsMode,
			Platform:       rc.Config.ContainerArchitecture,
			Options:        rc.options(ctx),
			AutoRemove:     rc.Config.AutoRemove,
			ValidVolumes:   rc.validVolumes(),
			AllocatePTY:    rc.Config.AllocatePTY,
		})
		if rc.JobContainer == nil {
			return errors.New("Failed to create job container")
		}

		defer printStartJobContainerGroup(ctx, image, name, networkName)()
		return common.NewPipelineExecutor(
			rc.pullServicesImages(rc.Config.ForcePull),
			rc.JobContainer.Pull(rc.Config.ForcePull),
			rc.stopJobContainer(),
			container.NewDockerNetworkCreateExecutor(networkName).IfBool(createAndDeleteNetwork),
			rc.startServiceContainers(networkName),
			rc.JobContainer.Create(rc.Config.ContainerCapAdd, rc.Config.ContainerCapDrop),
			rc.JobContainer.Start(false),
			rc.JobContainer.Copy(rc.JobContainer.GetActPath()+"/", &container.FileEntry{
				Name: "workflow/event.json",
				Mode: 0o644,
				Body: rc.EventJSON,
			}, &container.FileEntry{
				Name: "workflow/envs.txt",
				Mode: 0o666,
				Body: "",
			}),
		)(ctx)
	}
}

func (rc *RunContext) execJobContainer(cmd []string, env map[string]string, user, workdir string) common.Executor { //nolint:unparam // pre-existing issue from nektos/act
	return func(ctx context.Context) error {
		return rc.JobContainer.Exec(cmd, env, user, workdir)(ctx)
	}
}

func (rc *RunContext) ApplyExtraPath(ctx context.Context, env *map[string]string) {
	if len(rc.ExtraPath) > 0 {
		path := rc.JobContainer.GetPathVariableName()
		if rc.JobContainer.IsEnvironmentCaseInsensitive() {
			// On windows system Path and PATH could also be in the map
			for k := range *env {
				if strings.EqualFold(path, k) {
					path = k
					break
				}
			}
		}
		if (*env)[path] == "" {
			cenv := map[string]string{}
			var cpath string
			if err := rc.JobContainer.UpdateFromImageEnv(&cenv)(ctx); err == nil {
				if p, ok := cenv[path]; ok {
					cpath = p
				}
			}
			if len(cpath) == 0 {
				cpath = rc.JobContainer.DefaultPathVariable()
			}
			(*env)[path] = cpath
		}
		(*env)[path] = rc.JobContainer.JoinPathVariable(append(rc.ExtraPath, (*env)[path])...)
	}
}

func (rc *RunContext) UpdateExtraPath(ctx context.Context, githubEnvPath string) error {
	if common.Dryrun(ctx) {
		return nil
	}
	pathTar, err := rc.JobContainer.GetContainerArchive(ctx, githubEnvPath)
	if err != nil {
		return err
	}
	defer pathTar.Close()

	reader := tar.NewReader(pathTar)
	_, err = reader.Next()
	if err != nil && err != io.EOF {
		return err
	}
	s := bufio.NewScanner(reader)
	for s.Scan() {
		line := s.Text()
		if len(line) > 0 {
			rc.addPath(ctx, line)
		}
	}
	return nil
}

// stopJobContainer removes the job container (if it exists) and its volume (if it exists)
func (rc *RunContext) stopJobContainer() common.Executor {
	return func(ctx context.Context) error {
		if rc.cleanUpJobContainer != nil {
			return rc.cleanUpJobContainer(ctx)
		}
		return nil
	}
}

func (rc *RunContext) pullServicesImages(forcePull bool) common.Executor {
	return func(ctx context.Context) error {
		execs := []common.Executor{}
		for _, c := range rc.ServiceContainers {
			execs = append(execs, c.Pull(forcePull))
		}
		return common.NewParallelExecutor(len(execs), execs...)(ctx)
	}
}

func (rc *RunContext) startServiceContainers(_ string) common.Executor {
	return func(ctx context.Context) error {
		execs := []common.Executor{}
		for _, c := range rc.ServiceContainers {
			execs = append(execs, common.NewPipelineExecutor(
				c.Pull(false),
				c.Create(rc.Config.ContainerCapAdd, rc.Config.ContainerCapDrop),
				c.Start(false),
			))
		}
		return common.NewParallelExecutor(len(execs), execs...)(ctx)
	}
}

func (rc *RunContext) stopServiceContainers() common.Executor {
	return func(ctx context.Context) error {
		execs := []common.Executor{}
		for _, c := range rc.ServiceContainers {
			execs = append(execs, c.Remove().Finally(c.Close()))
		}
		return common.NewParallelExecutor(len(execs), execs...)(ctx)
	}
}

// Prepare the mounts and binds for the worker

// ActionCacheDir is for rc
func (rc *RunContext) ActionCacheDir() string {
	if rc.Config.ActionCacheDir != "" {
		return rc.Config.ActionCacheDir
	}
	var xdgCache string
	var ok bool
	if xdgCache, ok = os.LookupEnv("XDG_CACHE_HOME"); !ok || xdgCache == "" {
		if home, err := os.UserHomeDir(); err == nil {
			xdgCache = filepath.Join(home, ".cache")
		} else if xdgCache, err = filepath.Abs("."); err != nil {
			// It's almost impossible to get here, so the temp dir is a good fallback
			xdgCache = os.TempDir()
		}
	}
	return filepath.Join(xdgCache, "act")
}

// Interpolate outputs after a job is done
// jobMutexes serializes per-job result/output aggregation across the matrix combinations that
// share one *model.Job and run in parallel. Keyed by the shared *model.Job (mirrors the
// per-directory AcquireCloneLock pattern).
var jobMutexes sync.Map // key: *model.Job; value: *sync.Mutex

func lockJob(job *model.Job) func() {
	v, _ := jobMutexes.LoadOrStore(job, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (rc *RunContext) interpolateOutputs() common.Executor {
	return func(ctx context.Context) error {
		ee := rc.NewExpressionEvaluator(ctx)
		job := rc.Run.Job()
		// Matrix combinations share this Job and its Outputs map. Interpolate from this combo's
		// pristine snapshot (outputTemplate) and write under the lock, so each combo overwrites
		// with its own resolved values (last wins, as on GitHub) instead of the first combo's
		// resolved values freezing the shared template against later combos.
		defer lockJob(job)()
		for k, v := range rc.outputTemplate {
			job.Outputs[k] = ee.Interpolate(ctx, v)
		}
		return nil
	}
}

func (rc *RunContext) startContainer() common.Executor {
	return func(ctx context.Context) error {
		var err error
		if rc.IsHostEnv(ctx) {
			err = rc.startHostEnvironment()(ctx)
		} else {
			err = rc.startJobContainer()(ctx)
		}
		if err != nil {
			// The job executor's teardown only runs after a successful start, so a failed
			// start would otherwise leak the per-job network and container.
			rc.cleanupFailedStart(ctx)
		}
		return err
	}
}

func (rc *RunContext) cleanupFailedStart(ctx context.Context) {
	if rc.cleanUpJobContainer == nil {
		return
	}
	cleanCtx := ctx
	if ctx.Err() != nil {
		// the start likely failed because ctx was cancelled, detach so teardown still runs
		var cancel context.CancelFunc
		cleanCtx, cancel = context.WithTimeout(common.WithLogger(context.Background(), common.Logger(ctx)), time.Minute)
		defer cancel()
	}
	if err := rc.cleanUpJobContainer(cleanCtx); err != nil {
		common.Logger(ctx).Errorf("Error while cleaning up after failed container start for job %s: %v", rc.JobName, err)
	}
}

func (rc *RunContext) IsHostEnv(ctx context.Context) bool {
	platform := rc.runsOnImage(ctx)
	image := rc.containerImage(ctx)
	return image == "" && strings.EqualFold(platform, "-self-hosted")
}

func (rc *RunContext) stopContainer() common.Executor {
	return rc.stopJobContainer()
}

func (rc *RunContext) closeContainer() common.Executor {
	return func(ctx context.Context) error {
		if rc.JobContainer != nil {
			return rc.JobContainer.Close()(ctx)
		}
		return nil
	}
}

func (rc *RunContext) matrix() map[string]any {
	return rc.Matrix
}

func (rc *RunContext) result(result string) {
	rc.Run.Job().Result = result
}

func (rc *RunContext) steps() []*model.Step {
	// Return per-job copies of the steps. Matrix combinations run in parallel and share the
	// workflow model, but step execution mutates per-job fields and evaluates the If/Env nodes
	// in place, so the *model.Step instances must not be shared across jobs (see Step.Clone).
	shared := rc.Run.Job().Steps
	steps := make([]*model.Step, len(shared))
	for i, step := range shared {
		if step == nil {
			continue
		}
		steps[i] = step.Clone()
	}
	return steps
}

// Executor returns a pipeline executor for all the steps in the job
func (rc *RunContext) Executor() (common.Executor, error) {
	var executor common.Executor
	jobType, err := rc.Run.Job().Type()

	switch jobType {
	case model.JobTypeDefault:
		executor = newJobExecutor(rc, &stepFactoryImpl{}, rc)
	case model.JobTypeReusableWorkflowLocal:
		executor = newLocalReusableWorkflowExecutor(rc)
	case model.JobTypeReusableWorkflowRemote:
		executor = newRemoteReusableWorkflowExecutor(rc)
	case model.JobTypeInvalid:
		return nil, err
	}

	return func(ctx context.Context) error {
		res, err := rc.isEnabled(ctx)
		if err != nil {
			rc.caller.setReusedWorkflowJobResult(rc.JobName, "failure") // For Gitea
			return err
		}
		if res {
			return executor(ctx)
		}
		return nil
	}, nil
}

func (rc *RunContext) containerImage(ctx context.Context) string {
	job := rc.Run.Job()

	c := job.Container()
	if c != nil {
		return rc.ExprEval.Interpolate(ctx, c.Image)
	}

	return ""
}

func (rc *RunContext) runsOnImage(ctx context.Context) string {
	if rc.Run.Job().RunsOn() == nil {
		common.Logger(ctx).Errorf("'runs-on' key not defined in %s", rc.String())
	}

	job := rc.Run.Job()
	runsOn := job.RunsOn()
	for i, v := range runsOn {
		runsOn[i] = rc.ExprEval.Interpolate(ctx, v)
	}

	if pick := rc.Config.PlatformPicker; pick != nil {
		if image := pick(runsOn); image != "" {
			return image
		}
	}

	for _, platformName := range rc.runsOnPlatformNames(ctx) {
		image := rc.Config.Platforms[strings.ToLower(platformName)]
		if image != "" {
			return image
		}
	}

	return ""
}

func (rc *RunContext) runsOnPlatformNames(ctx context.Context) []string {
	job := rc.Run.Job()

	if job.RunsOn() == nil {
		return []string{}
	}

	// Evaluate a copy: RawRunsOn is shared across parallel matrix jobs, so interpolating it in
	// place would race and leak one matrix combination's runs-on into the others.
	rawRunsOn := model.CloneYamlNode(job.RawRunsOn)
	if err := rc.ExprEval.EvaluateYamlNode(ctx, &rawRunsOn); err != nil {
		common.Logger(ctx).Errorf("Error while evaluating runs-on: %v", err)
		return []string{}
	}

	return model.RunsOnFromNode(rawRunsOn)
}

func (rc *RunContext) platformImage(ctx context.Context) string {
	if containerImage := rc.containerImage(ctx); containerImage != "" {
		return containerImage
	}

	return rc.runsOnImage(ctx)
}

func (rc *RunContext) options(ctx context.Context) string {
	job := rc.Run.Job()
	c := job.Container()
	if c != nil {
		return rc.Config.ContainerOptions + " " + rc.ExprEval.Interpolate(ctx, c.Options)
	}

	return rc.Config.ContainerOptions
}

func (rc *RunContext) isEnabled(ctx context.Context) (bool, error) {
	job := rc.Run.Job()
	l := common.Logger(ctx)
	runJob, runJobErr := EvalBool(ctx, rc.ExprEval, job.If.Value, exprparser.DefaultStatusCheckSuccess)
	jobType, jobTypeErr := job.Type()

	if runJobErr != nil {
		return false, fmt.Errorf("if-expression %q evaluation failed: %s", job.If.Value, runJobErr)
	}

	if jobType == model.JobTypeInvalid {
		return false, jobTypeErr
	}

	if !runJob {
		if rc.caller != nil { // For Gitea
			rc.caller.setReusedWorkflowJobResult(rc.JobName, "skipped")
			return false, nil
		}
		l.WithField("jobResult", "skipped").Debugf("Skipping job '%s' due to '%s'", job.Name, job.If.Value)
		return false, nil
	}

	if jobType != model.JobTypeDefault {
		return true, nil
	}

	img := rc.platformImage(ctx)
	if img == "" {
		for _, platformName := range rc.runsOnPlatformNames(ctx) {
			l.Infof("Skipping unsupported platform -- Try running with `-P %+v=...`", platformName)
		}
		return false, nil
	}
	return true, nil
}

func mergeMaps(maps ...map[string]string) map[string]string {
	rtnMap := make(map[string]string)
	for _, m := range maps {
		maps0.Copy(rtnMap, m)
	}
	return rtnMap
}

func createContainerName(parts ...string) string {
	name := strings.Join(parts, "-")
	pattern := regexp.MustCompile("[^a-zA-Z0-9]")
	name = pattern.ReplaceAllString(name, "-")
	name = strings.ReplaceAll(name, "--", "-")
	hash := sha256.Sum256([]byte(name))

	// SHA256 is 64 hex characters. So trim name to 63 characters to make room for the hash and separator
	trimmedName := strings.Trim(trimToLen(name, 63), "-")

	return fmt.Sprintf("%s-%x", trimmedName, hash)
}

func trimToLen(s string, l int) string {
	if l < 0 {
		l = 0
	}
	if len(s) > l {
		return s[:l]
	}
	return s
}

func (rc *RunContext) getJobContext() *model.JobContext {
	jobStatus := "success"
	for _, stepStatus := range rc.StepResults {
		if stepStatus.Conclusion == model.StepStatusFailure {
			jobStatus = "failure"
			break
		}
	}
	return &model.JobContext{
		Status: jobStatus,
	}
}

func (rc *RunContext) getStepsContext() map[string]*model.StepResult {
	return rc.StepResults
}

func (rc *RunContext) getGithubContext(ctx context.Context) *model.GithubContext {
	logger := common.Logger(ctx)
	ghc := &model.GithubContext{
		Event:            make(map[string]any),
		Workflow:         rc.Run.Workflow.Name,
		RunID:            rc.Config.Env["GITHUB_RUN_ID"],
		RunNumber:        rc.Config.Env["GITHUB_RUN_NUMBER"],
		Actor:            rc.Config.Actor,
		EventName:        rc.Config.EventName,
		Action:           rc.CurrentStep,
		Token:            rc.Config.Token,
		Job:              rc.Run.JobID,
		ActionPath:       rc.ActionPath,
		ActionRepository: rc.Env["GITHUB_ACTION_REPOSITORY"],
		ActionRef:        rc.Env["GITHUB_ACTION_REF"],
		RepositoryOwner:  rc.Config.Env["GITHUB_REPOSITORY_OWNER"],
		RetentionDays:    rc.Config.Env["GITHUB_RETENTION_DAYS"],
		RunnerPerflog:    rc.Config.Env["RUNNER_PERFLOG"],
		RunnerTrackingID: rc.Config.Env["RUNNER_TRACKING_ID"],
		Repository:       rc.Config.Env["GITHUB_REPOSITORY"],
		Ref:              rc.Config.Env["GITHUB_REF"],
		Sha:              rc.Config.Env["SHA_REF"],
		RefName:          rc.Config.Env["GITHUB_REF_NAME"],
		RefType:          rc.Config.Env["GITHUB_REF_TYPE"],
		BaseRef:          rc.Config.Env["GITHUB_BASE_REF"],
		HeadRef:          rc.Config.Env["GITHUB_HEAD_REF"],
		Workspace:        rc.Config.Env["GITHUB_WORKSPACE"],
	}
	if rc.JobContainer != nil {
		ghc.EventPath = rc.JobContainer.GetActPath() + "/workflow/event.json"
		ghc.Workspace = rc.JobContainer.ToContainerPath(rc.Config.Workdir)
	}

	if ghc.RunID == "" {
		ghc.RunID = "1"
	}

	if ghc.RunNumber == "" {
		ghc.RunNumber = "1"
	}

	if ghc.RetentionDays == "" {
		ghc.RetentionDays = "0"
	}

	if ghc.RunnerPerflog == "" {
		ghc.RunnerPerflog = "/dev/null"
	}

	// Backwards compatibility for configs that require
	// a default rather than being run as a cmd
	if ghc.Actor == "" {
		ghc.Actor = "nektos/act"
	}

	{ // Adapt to Gitea
		if preset := rc.Config.PresetGitHubContext; preset != nil {
			ghc.Event = preset.Event
			ghc.RunID = preset.RunID
			ghc.RunNumber = preset.RunNumber
			ghc.RunAttempt = preset.RunAttempt
			ghc.Actor = preset.Actor
			ghc.Repository = preset.Repository
			ghc.EventName = preset.EventName
			ghc.Sha = preset.Sha
			ghc.Ref = preset.Ref
			ghc.RefName = preset.RefName
			ghc.RefType = preset.RefType
			ghc.HeadRef = preset.HeadRef
			ghc.BaseRef = preset.BaseRef
			ghc.Token = preset.Token
			ghc.RepositoryOwner = preset.RepositoryOwner
			ghc.RetentionDays = preset.RetentionDays

			instance := rc.Config.GitHubInstance
			if !strings.HasPrefix(instance, "http://") &&
				!strings.HasPrefix(instance, "https://") {
				instance = "https://" + instance
			}
			ghc.ServerURL = instance
			ghc.APIURL = instance + "/api/v1" // the version of Gitea is v1
			ghc.GraphQLURL = ""               // Gitea doesn't support graphql
			return ghc
		}
	}

	if rc.EventJSON != "" {
		err := json.Unmarshal([]byte(rc.EventJSON), &ghc.Event)
		if err != nil {
			logger.Errorf("Unable to Unmarshal event '%s': %v", rc.EventJSON, err)
		}
	}

	ghc.SetBaseAndHeadRef()
	repoPath := rc.Config.Workdir
	ghc.SetRepositoryAndOwner(ctx, rc.Config.GitHubInstance, rc.Config.RemoteName, repoPath)
	if ghc.Ref == "" {
		ghc.SetRef(ctx, rc.Config.DefaultBranch, repoPath)
	}
	if ghc.Sha == "" {
		ghc.SetSha(ctx, repoPath)
	}

	ghc.SetRefTypeAndName()

	// defaults
	ghc.ServerURL = "https://github.com"
	ghc.APIURL = "https://api.github.com"
	ghc.GraphQLURL = "https://api.github.com/graphql"
	// per GHES
	if rc.Config.GitHubInstance != "github.com" {
		ghc.ServerURL = "https://" + rc.Config.GitHubInstance
		ghc.APIURL = fmt.Sprintf("https://%s/api/v3", rc.Config.GitHubInstance)
		ghc.GraphQLURL = fmt.Sprintf("https://%s/api/graphql", rc.Config.GitHubInstance)
	}

	{ // Adapt to Gitea
		instance := rc.Config.GitHubInstance
		if !strings.HasPrefix(instance, "http://") &&
			!strings.HasPrefix(instance, "https://") {
			instance = "https://" + instance
		}
		ghc.ServerURL = instance
		ghc.APIURL = instance + "/api/v1" // the version of Gitea is v1
		ghc.GraphQLURL = ""               // Gitea doesn't support graphql
	}

	// allow to be overridden by user
	if rc.Config.Env["GITHUB_SERVER_URL"] != "" {
		ghc.ServerURL = rc.Config.Env["GITHUB_SERVER_URL"]
	}
	if rc.Config.Env["GITHUB_API_URL"] != "" {
		ghc.APIURL = rc.Config.Env["GITHUB_API_URL"]
	}
	if rc.Config.Env["GITHUB_GRAPHQL_URL"] != "" {
		ghc.GraphQLURL = rc.Config.Env["GITHUB_GRAPHQL_URL"]
	}

	return ghc
}

func isLocalCheckout(ghc *model.GithubContext, step *model.Step) bool {
	if step.Type() == model.StepTypeInvalid {
		// This will be errored out by the executor later, we need this here to avoid a null panic though
		return false
	}
	if step.Type() != model.StepTypeUsesActionRemote {
		return false
	}
	remoteAction := newRemoteAction(step.Uses)
	if remoteAction == nil {
		// IsCheckout() will nil panic if we dont bail out early
		return false
	}
	if !remoteAction.IsCheckout() {
		return false
	}

	if repository, ok := step.With["repository"]; ok && repository != ghc.Repository {
		return false
	}
	if repository, ok := step.With["ref"]; ok && repository != ghc.Ref {
		return false
	}
	return true
}

func nestedMapLookup(m map[string]any, ks ...string) (rval any) {
	var ok bool

	if len(ks) == 0 { // degenerate input
		return nil
	}
	if rval, ok = m[ks[0]]; !ok {
		return nil
	} else if len(ks) == 1 { // we've reached the final key
		return rval
	} else if m, ok = rval.(map[string]any); !ok {
		return nil
	} else { // 1+ more keys
		return nestedMapLookup(m, ks[1:]...)
	}
}

func (rc *RunContext) withGithubEnv(ctx context.Context, github *model.GithubContext, env map[string]string) map[string]string { //nolint:unparam // pre-existing issue from nektos/act
	env["CI"] = "true"
	env["GITHUB_WORKFLOW"] = github.Workflow
	env["GITHUB_RUN_ID"] = github.RunID
	env["GITHUB_RUN_NUMBER"] = github.RunNumber
	env["GITHUB_ACTION"] = github.Action
	env["GITHUB_ACTION_PATH"] = github.ActionPath
	env["GITHUB_ACTION_REPOSITORY"] = github.ActionRepository
	env["GITHUB_ACTION_REF"] = github.ActionRef
	env["GITHUB_ACTIONS"] = "true"
	env["GITHUB_ACTOR"] = github.Actor
	env["GITHUB_REPOSITORY"] = github.Repository
	env["GITHUB_EVENT_NAME"] = github.EventName
	env["GITHUB_EVENT_PATH"] = github.EventPath
	env["GITHUB_WORKSPACE"] = github.Workspace
	env["GITHUB_SHA"] = github.Sha
	env["GITHUB_REF"] = github.Ref
	env["GITHUB_REF_NAME"] = github.RefName
	env["GITHUB_REF_TYPE"] = github.RefType
	env["GITHUB_JOB"] = github.Job
	env["GITHUB_REPOSITORY_OWNER"] = github.RepositoryOwner
	env["GITHUB_RETENTION_DAYS"] = github.RetentionDays
	env["RUNNER_PERFLOG"] = github.RunnerPerflog
	env["RUNNER_TRACKING_ID"] = github.RunnerTrackingID
	env["GITHUB_BASE_REF"] = github.BaseRef
	env["GITHUB_HEAD_REF"] = github.HeadRef
	env["GITHUB_SERVER_URL"] = github.ServerURL
	env["GITHUB_API_URL"] = github.APIURL
	env["GITHUB_GRAPHQL_URL"] = github.GraphQLURL

	{ // Adapt to Gitea
		instance := rc.Config.GitHubInstance
		if !strings.HasPrefix(instance, "http://") &&
			!strings.HasPrefix(instance, "https://") {
			instance = "https://" + instance
		}
		env["GITHUB_SERVER_URL"] = instance
		env["GITHUB_API_URL"] = instance + "/api/v1" // the version of Gitea is v1
		env["GITHUB_GRAPHQL_URL"] = ""               // Gitea doesn't support graphql
		env["GITHUB_RUN_ATTEMPT"] = github.RunAttempt
	}

	if rc.Config.ArtifactServerPath != "" {
		setActionRuntimeVars(rc, env)
	}

	for _, platformName := range rc.runsOnPlatformNames(ctx) {
		if platformName != "" {
			if platformName == "ubuntu-latest" {
				// hardcode current ubuntu-latest since we have no way to check that 'on the fly'
				env["ImageOS"] = "ubuntu20"
			} else {
				platformName = strings.SplitN(strings.Replace(platformName, `-`, ``, 1), `.`, 2)[0]
				env["ImageOS"] = platformName
			}
		}
	}

	return env
}

func setActionRuntimeVars(rc *RunContext, env map[string]string) {
	actionsRuntimeURL := os.Getenv("ACTIONS_RUNTIME_URL")
	if actionsRuntimeURL == "" {
		actionsRuntimeURL = fmt.Sprintf("http://%s:%s/", rc.Config.ArtifactServerAddr, rc.Config.ArtifactServerPort)
	}
	env["ACTIONS_RUNTIME_URL"] = actionsRuntimeURL

	actionsRuntimeToken := os.Getenv("ACTIONS_RUNTIME_TOKEN")
	if actionsRuntimeToken == "" {
		actionsRuntimeToken = "token"
	}
	env["ACTIONS_RUNTIME_TOKEN"] = actionsRuntimeToken
}

func (rc *RunContext) handleCredentials(ctx context.Context) (string, string, error) {
	// TODO: remove below 2 lines when we can release act with breaking changes
	username := rc.Config.Secrets["DOCKER_USERNAME"]
	password := rc.Config.Secrets["DOCKER_PASSWORD"]

	container := rc.Run.Job().Container()
	if container == nil || container.Credentials == nil {
		return username, password, nil
	}

	if container.Credentials != nil && len(container.Credentials) != 2 {
		err := errors.New("invalid property count for key 'credentials:'")
		return "", "", err
	}

	ee := rc.NewExpressionEvaluator(ctx)
	if username = ee.Interpolate(ctx, container.Credentials["username"]); username == "" {
		err := errors.New("failed to interpolate container.credentials.username")
		return "", "", err
	}
	if password = ee.Interpolate(ctx, container.Credentials["password"]); password == "" {
		err := errors.New("failed to interpolate container.credentials.password")
		return "", "", err
	}

	if container.Credentials["username"] == "" || container.Credentials["password"] == "" {
		err := errors.New("container.credentials cannot be empty")
		return "", "", err
	}

	return username, password, nil
}

func (rc *RunContext) handleServiceCredentials(ctx context.Context, creds map[string]string) (username, password string, err error) {
	if creds == nil {
		return username, password, err
	}
	if len(creds) != 2 {
		err = errors.New("invalid property count for key 'credentials:'")
		return username, password, err
	}

	ee := rc.NewExpressionEvaluator(ctx)
	if username = ee.Interpolate(ctx, creds["username"]); username == "" {
		err = errors.New("failed to interpolate credentials.username")
		return username, password, err
	}

	if password = ee.Interpolate(ctx, creds["password"]); password == "" {
		err = errors.New("failed to interpolate credentials.password")
		return username, password, err
	}

	return username, password, err
}

// GetServiceBindsAndMounts returns the binds and mounts for the service container, resolving paths as appopriate
func (rc *RunContext) GetServiceBindsAndMounts(svcVolumes []string) ([]string, map[string]string) {
	binds := []string{}
	if daemonSocket := rc.containerDaemonSocket(); daemonSocket != "-" {
		daemonPath := getDockerDaemonSocketMountPath(daemonSocket)
		binds = append(binds, fmt.Sprintf("%s:%s", daemonPath, "/var/run/docker.sock"))
	}

	mounts := map[string]string{}

	for _, v := range svcVolumes {
		if !strings.Contains(v, ":") || filepath.IsAbs(v) {
			// Bind anonymous volume or host file.
			binds = append(binds, v)
		} else {
			// Mount existing volume.
			paths := strings.SplitN(v, ":", 2)
			mounts[paths[0]] = paths[1]
		}
	}

	return binds, mounts
}
