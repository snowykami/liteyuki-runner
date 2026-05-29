// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !(WITHOUT_DOCKER || !(linux || darwin || windows || netbsd))

package container

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/filecollector"

	"dario.cat/mergo"
	"github.com/Masterminds/semver"
	"github.com/docker/cli/cli/compose/loader"
	"github.com/docker/cli/cli/connhelper"
	"github.com/go-git/go-billy/v5/helper/polyfill"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/gobwas/glob"
	"github.com/joho/godotenv"
	"github.com/kballard/go-shellquote"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/pflag"
)

// NewContainer creates a reference to a container
func NewContainer(input *NewContainerInput) ExecutionsEnvironment {
	cr := new(containerReference)
	cr.input = input
	return cr
}

func (cr *containerReference) ConnectToNetwork(name string) common.Executor {
	return common.
		NewDebugExecutor("docker network connect %s %s", name, cr.input.Name).
		Then(
			common.NewPipelineExecutor(
				cr.connect(),
				cr.connectToNetwork(name, cr.input.NetworkAliases),
			).IfNot(common.Dryrun),
		)
}

func (cr *containerReference) connectToNetwork(name string, aliases []string) common.Executor {
	return func(ctx context.Context) error {
		_, err := cr.cli.NetworkConnect(ctx, name, client.NetworkConnectOptions{
			Container: cr.input.Name,
			EndpointConfig: &network.EndpointSettings{
				Aliases: aliases,
			},
		})
		return err
	}
}

// supportsContainerImagePlatform returns true if the underlying Docker server
// API version is 1.41 and beyond
func supportsContainerImagePlatform(ctx context.Context, cli client.APIClient) bool {
	logger := common.Logger(ctx)
	ver, err := cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		logger.Panicf("Failed to get Docker API Version: %s", err)
		return false
	}
	sv, err := semver.NewVersion(ver.APIVersion)
	if err != nil {
		logger.Panicf("Failed to unmarshal Docker Version: %s", err)
		return false
	}
	constraint, _ := semver.NewConstraint(">= 1.41")
	return constraint.Check(sv)
}

func (cr *containerReference) Create(capAdd, capDrop []string) common.Executor {
	return common.
		NewInfoExecutor("docker create image=%s platform=%s entrypoint=%+q cmd=%+q network=%+q", cr.input.Image, cr.input.Platform, cr.input.Entrypoint, cr.input.Cmd, cr.input.NetworkMode).
		Then(
			common.NewPipelineExecutor(
				cr.connect(),
				cr.find(),
				cr.create(capAdd, capDrop),
			).IfNot(common.Dryrun),
		)
}

func (cr *containerReference) Start(attach bool) common.Executor {
	return common.
		NewInfoExecutor("docker run image=%s platform=%s entrypoint=%+q cmd=%+q network=%+q", cr.input.Image, cr.input.Platform, cr.input.Entrypoint, cr.input.Cmd, cr.input.NetworkMode).
		Then(
			common.NewPipelineExecutor(
				cr.connect(),
				cr.find(),
				cr.attach().IfBool(attach),
				cr.start(),
				cr.wait().IfBool(attach),
				cr.tryReadUID(),
				cr.tryReadGID(),
				func(ctx context.Context) error {
					// If this fails, then folders have wrong permissions on non root container
					if cr.UID != 0 || cr.GID != 0 {
						_ = cr.Exec([]string{"chown", "-R", fmt.Sprintf("%d:%d", cr.UID, cr.GID), cr.input.WorkingDir}, nil, "0", "")(ctx)
					}
					return nil
				},
			).IfNot(common.Dryrun),
		)
}

func (cr *containerReference) Pull(forcePull bool) common.Executor {
	return common.
		NewInfoExecutor("docker pull image=%s platform=%s username=%s forcePull=%t", cr.input.Image, cr.input.Platform, cr.input.Username, forcePull).
		Then(
			NewDockerPullExecutor(NewDockerPullExecutorInput{
				Image:     cr.input.Image,
				ForcePull: forcePull,
				Platform:  cr.input.Platform,
				Username:  cr.input.Username,
				Password:  cr.input.Password,
			}),
		)
}

func (cr *containerReference) Copy(destPath string, files ...*FileEntry) common.Executor {
	return common.NewPipelineExecutor(
		cr.connect(),
		cr.find(),
		cr.copyContent(destPath, files...),
	).IfNot(common.Dryrun)
}

func (cr *containerReference) CopyDir(destPath, srcPath string, useGitIgnore bool) common.Executor {
	return common.NewPipelineExecutor(
		common.NewInfoExecutor("docker cp src=%s dst=%s", srcPath, destPath),
		cr.copyDir(destPath, srcPath, useGitIgnore),
		func(ctx context.Context) error {
			// If this fails, then folders have wrong permissions on non root container
			if cr.UID != 0 || cr.GID != 0 {
				_ = cr.Exec([]string{"chown", "-R", fmt.Sprintf("%d:%d", cr.UID, cr.GID), destPath}, nil, "0", "")(ctx)
			}
			return nil
		},
	).IfNot(common.Dryrun)
}

func (cr *containerReference) GetContainerArchive(ctx context.Context, srcPath string) (io.ReadCloser, error) {
	if common.Dryrun(ctx) {
		return nil, errors.New("DRYRUN is not supported in GetContainerArchive")
	}
	result, err := cr.cli.CopyFromContainer(ctx, cr.id, client.CopyFromContainerOptions{SourcePath: srcPath})
	if err != nil {
		return nil, err
	}
	return result.Content, nil
}

func (cr *containerReference) UpdateFromEnv(srcPath string, env *map[string]string) common.Executor {
	return parseEnvFile(cr, srcPath, env).IfNot(common.Dryrun)
}

func (cr *containerReference) UpdateFromImageEnv(env *map[string]string) common.Executor {
	return cr.extractFromImageEnv(env).IfNot(common.Dryrun)
}

func (cr *containerReference) Exec(command []string, env map[string]string, user, workdir string) common.Executor {
	return common.NewPipelineExecutor(
		common.NewInfoExecutor("docker exec cmd=[%s] user=%s workdir=%s", strings.Join(command, " "), user, workdir),
		cr.connect(),
		cr.find(),
		cr.exec(command, env, user, workdir),
	).IfNot(common.Dryrun)
}

func (cr *containerReference) Remove() common.Executor {
	return common.NewPipelineExecutor(
		cr.connect(),
		cr.find(),
	).Finally(
		cr.remove(),
	).IfNot(common.Dryrun)
}

func (cr *containerReference) ReplaceLogWriter(stdout, stderr io.Writer) (io.Writer, io.Writer) {
	out := cr.input.Stdout
	err := cr.input.Stderr

	cr.input.Stdout = stdout
	cr.input.Stderr = stderr

	return out, err
}

type containerReference struct {
	cli   client.APIClient
	id    string
	input *NewContainerInput
	UID   int
	GID   int
	LinuxContainerEnvironmentExtensions
}

func GetDockerClient(ctx context.Context) (cli client.APIClient, err error) {
	dockerHost := os.Getenv("DOCKER_HOST")

	if strings.HasPrefix(dockerHost, "ssh://") {
		var helper *connhelper.ConnectionHelper

		helper, err = connhelper.GetConnectionHelper(dockerHost)
		if err != nil {
			return nil, err
		}
		cli, err = client.New(
			client.WithHost(helper.Host),
			client.WithDialContext(helper.Dialer),
		)
	} else {
		cli, err = client.New(client.FromEnv)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect to docker daemon: %w", err)
	}
	// Best-effort API version negotiation, matching the old client.NegotiateAPIVersion
	// behaviour. Ping failures here are non-fatal: the connection is exercised again on
	// the first real call, and the client falls back to the daemon's default API version.
	if _, err := cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		common.Logger(ctx).Warnf("docker daemon ping during version negotiation failed, continuing: %v", err)
	}

	return cli, nil
}

func GetHostInfo(ctx context.Context) (info system.Info, err error) {
	var cli client.APIClient
	cli, err = GetDockerClient(ctx)
	if err != nil {
		return info, err
	}
	defer cli.Close()

	result, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return info, err
	}

	return result.Info, nil
}

// Arch fetches values from docker info and translates architecture to
// GitHub actions compatible runner.arch values
// https://github.com/github/docs/blob/main/data/reusables/actions/runner-arch-description.md
func RunnerArch(ctx context.Context) string {
	info, err := GetHostInfo(ctx)
	if err != nil {
		return ""
	}

	archMapper := map[string]string{
		"x86_64":  "X64",
		"amd64":   "X64",
		"386":     "X86",
		"aarch64": "ARM64",
		"arm64":   "ARM64",
	}
	if arch, ok := archMapper[info.Architecture]; ok {
		return arch
	}
	return info.Architecture
}

func (cr *containerReference) connect() common.Executor {
	return func(ctx context.Context) error {
		if cr.cli != nil {
			return nil
		}
		cli, err := GetDockerClient(ctx)
		if err != nil {
			return err
		}
		cr.cli = cli
		return nil
	}
}

func (cr *containerReference) Close() common.Executor {
	return func(ctx context.Context) error {
		if cr.cli != nil {
			err := cr.cli.Close()
			cr.cli = nil
			if err != nil {
				return fmt.Errorf("failed to close client: %w", err)
			}
		}
		return nil
	}
}

func (cr *containerReference) find() common.Executor {
	return func(ctx context.Context) error {
		if cr.id != "" {
			return nil
		}
		containers, err := cr.cli.ContainerList(ctx, client.ContainerListOptions{
			All: true,
		})
		if err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}

		for _, c := range containers.Items {
			for _, name := range c.Names {
				if name[1:] == cr.input.Name {
					cr.id = c.ID
					return nil
				}
			}
		}

		cr.id = ""
		return nil
	}
}

func (cr *containerReference) remove() common.Executor {
	return func(ctx context.Context) error {
		if cr.id == "" {
			return nil
		}

		logger := common.Logger(ctx)
		_, err := cr.cli.ContainerRemove(ctx, cr.id, client.ContainerRemoveOptions{
			RemoveVolumes: true,
			Force:         true,
		})
		if err != nil {
			logger.Error(fmt.Errorf("failed to remove container: %w", err))
		}

		logger.Debugf("Removed container: %v", cr.id)
		cr.id = ""
		return nil
	}
}

func (cr *containerReference) mergeContainerConfigs(ctx context.Context, config *container.Config, hostConfig *container.HostConfig) (*container.Config, *container.HostConfig, error) {
	logger := common.Logger(ctx)
	input := cr.input

	if input.Options == "" {
		return config, hostConfig, nil
	}

	// parse configuration from CLI container.options
	flags := pflag.NewFlagSet("container_flags", pflag.ContinueOnError)
	copts := addFlags(flags)

	optionsArgs, err := shellquote.Split(input.Options)
	if err != nil {
		return nil, nil, fmt.Errorf("Cannot split container options: '%s': '%w'", input.Options, err)
	}

	err = flags.Parse(optionsArgs)
	if err != nil {
		return nil, nil, fmt.Errorf("Cannot parse container options: '%s': '%w'", input.Options, err)
	}

	// FIXME: If everything is fine after gitea/act v0.260.0, remove the following comment.
	// In the old fork version, the code is
	// if len(copts.netMode.Value()) == 0 {
	// 	if err = copts.netMode.Set("host"); err != nil {
	// 		return nil, nil, fmt.Errorf("Cannot parse networkmode=host. This is an internal error and should not happen: '%w'", err)
	// 	}
	// }
	// And it has been commented with:
	//   If a service container's network is set to `host`, the container will not be able to
	//   connect to the specified network created for the job container and the service containers.
	//   So comment out the following code.
	// Not the if it's necessary to comment it in the new version,
	// since it's cr.input.NetworkMode now.

	if len(copts.netMode.Value()) == 0 {
		if err = copts.netMode.Set(cr.input.NetworkMode); err != nil {
			return nil, nil, fmt.Errorf("Cannot parse networkmode=%s. This is an internal error and should not happen: '%w'", cr.input.NetworkMode, err)
		}
	}

	// If the `privileged` config has been disabled, `copts.privileged` need to be forced to false,
	// even if the user specifies `--privileged` in the options string.
	if !hostConfig.Privileged {
		copts.privileged = false
	}

	containerConfig, err := parse(flags, copts, runtime.GOOS)
	if err != nil {
		return nil, nil, fmt.Errorf("Cannot process container options: '%s': '%w'", input.Options, err)
	}

	logger.Debugf("Custom container.Config from options ==> %+v", containerConfig.Config)

	err = mergo.Merge(config, containerConfig.Config, mergo.WithOverride, mergo.WithAppendSlice)
	if err != nil {
		return nil, nil, fmt.Errorf("Cannot merge container.Config options: '%s': '%w'", input.Options, err)
	}
	logger.Debugf("Merged container.Config ==> %+v", config)

	logger.Debugf("Custom container.HostConfig from options ==> %+v", containerConfig.HostConfig)

	hostConfig.Binds = append(hostConfig.Binds, containerConfig.HostConfig.Binds...)
	hostConfig.Mounts = append(hostConfig.Mounts, containerConfig.HostConfig.Mounts...)
	binds := hostConfig.Binds
	mounts := hostConfig.Mounts
	networkMode := hostConfig.NetworkMode
	err = mergo.Merge(hostConfig, containerConfig.HostConfig, mergo.WithOverride)
	if err != nil {
		return nil, nil, fmt.Errorf("Cannot merge container.HostConfig options: '%s': '%w'", input.Options, err)
	}
	hostConfig.Binds = binds
	hostConfig.Mounts = mounts
	if len(copts.netMode.Value()) > 0 {
		logger.Warn("--network and --net in the options will be ignored.")
	}
	hostConfig.NetworkMode = networkMode
	logger.Debugf("Merged container.HostConfig ==> %+v", hostConfig)

	return config, hostConfig, nil
}

func (cr *containerReference) create(capAdd, capDrop []string) common.Executor {
	return func(ctx context.Context) error {
		if cr.id != "" {
			return nil
		}
		logger := common.Logger(ctx)
		input := cr.input
		exposedPorts, err := convertPortSet(input.ExposedPorts)
		if err != nil {
			return err
		}
		portBindings, err := convertPortMap(input.PortBindings)
		if err != nil {
			return err
		}

		config := &container.Config{
			Image:        input.Image,
			WorkingDir:   input.WorkingDir,
			Env:          input.Env,
			ExposedPorts: exposedPorts,
			Tty:          input.AllocatePTY,
		}
		// For Gitea, reduce log noise
		// logger.Debugf("Common container.Config ==> %+v", config)

		if len(input.Cmd) != 0 {
			config.Cmd = input.Cmd
		}

		if len(input.Entrypoint) != 0 {
			config.Entrypoint = input.Entrypoint
		}

		mounts := make([]mount.Mount, 0)
		for mountSource, mountTarget := range input.Mounts {
			mounts = append(mounts, mount.Mount{
				Type:   mount.TypeVolume,
				Source: mountSource,
				Target: mountTarget,
			})
		}

		var platSpecs *specs.Platform
		if supportsContainerImagePlatform(ctx, cr.cli) && cr.input.Platform != "" {
			platSpecs, err = parsePlatform(cr.input.Platform)
			if err != nil {
				return err
			}
		}

		hostConfig := &container.HostConfig{
			CapAdd:       capAdd,
			CapDrop:      capDrop,
			Binds:        input.Binds,
			Mounts:       mounts,
			NetworkMode:  container.NetworkMode(input.NetworkMode),
			Privileged:   input.Privileged,
			UsernsMode:   container.UsernsMode(input.UsernsMode),
			PortBindings: portBindings,
			AutoRemove:   input.AutoRemove,
		}
		// For Gitea, reduce log noise
		// logger.Debugf("Common container.HostConfig ==> %+v", hostConfig)

		config, hostConfig, err = cr.mergeContainerConfigs(ctx, config, hostConfig)
		if err != nil {
			return err
		}

		// For Gitea
		config, hostConfig = cr.sanitizeConfig(ctx, config, hostConfig)

		var networkingConfig *network.NetworkingConfig
		// For Gitea, reduce log noise
		// logger.Debugf("input.NetworkAliases ==> %v", input.NetworkAliases)
		n := hostConfig.NetworkMode
		// IsUserDefined and IsHost are broken on windows
		if n.IsUserDefined() && n != "host" && len(input.NetworkAliases) > 0 {
			endpointConfig := &network.EndpointSettings{
				Aliases: input.NetworkAliases,
			}
			networkingConfig = &network.NetworkingConfig{
				EndpointsConfig: map[string]*network.EndpointSettings{
					input.NetworkMode: endpointConfig,
				},
			}
		}

		resp, err := cr.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
			Config:           config,
			HostConfig:       hostConfig,
			NetworkingConfig: networkingConfig,
			Platform:         platSpecs,
			Name:             input.Name,
		})
		if err != nil {
			return fmt.Errorf("failed to create container: '%w'", err)
		}

		logger.Debugf("Created container name=%s id=%v from image %v (platform: %s)", input.Name, resp.ID, input.Image, input.Platform)
		logger.Debugf("ENV ==> %v", input.Env)

		cr.id = resp.ID
		return nil
	}
}

func (cr *containerReference) extractFromImageEnv(env *map[string]string) common.Executor {
	envMap := *env
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)

		inspect, err := cr.cli.ImageInspect(ctx, cr.input.Image)
		if err != nil {
			logger.Error(err)
			return fmt.Errorf("inspect image: %w", err)
		}

		if inspect.Config == nil {
			return nil
		}

		imageEnv, err := godotenv.Unmarshal(strings.Join(inspect.Config.Env, "\n"))
		if err != nil {
			logger.Error(err)
			return fmt.Errorf("unmarshal image env: %w", err)
		}

		for k, v := range imageEnv {
			if k == "PATH" {
				if envMap[k] == "" {
					envMap[k] = v
				} else {
					envMap[k] += `:` + v
				}
			} else if envMap[k] == "" {
				envMap[k] = v
			}
		}

		env = &envMap
		return nil
	}
}

func (cr *containerReference) exec(cmd []string, env map[string]string, user, workdir string) common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		// Fix slashes when running on Windows
		if runtime.GOOS == "windows" {
			var newCmd []string
			for _, v := range cmd {
				newCmd = append(newCmd, strings.ReplaceAll(v, `\`, `/`))
			}
			cmd = newCmd
		}

		logger.Debugf("Exec command '%s'", cmd)
		isTerminal := cr.input.AllocatePTY
		envList := make([]string, 0)
		for k, v := range env {
			envList = append(envList, fmt.Sprintf("%s=%s", k, v))
		}

		var wd string
		if workdir != "" {
			if strings.HasPrefix(workdir, "/") {
				wd = workdir
			} else {
				wd = fmt.Sprintf("%s/%s", cr.input.WorkingDir, workdir)
			}
		} else {
			wd = cr.input.WorkingDir
		}
		logger.Debugf("Working directory '%s'", wd)

		idResp, err := cr.cli.ExecCreate(ctx, cr.id, client.ExecCreateOptions{
			User:         user,
			Cmd:          cmd,
			WorkingDir:   wd,
			Env:          envList,
			TTY:          isTerminal,
			AttachStderr: true,
			AttachStdout: true,
		})
		if err != nil {
			return fmt.Errorf("failed to create exec: %w", err)
		}

		resp, err := cr.cli.ExecAttach(ctx, idResp.ID, client.ExecAttachOptions{
			TTY: isTerminal,
		})
		if err != nil {
			return fmt.Errorf("failed to attach to exec: %w", err)
		}
		defer resp.Close()

		err = cr.waitForCommand(ctx, isTerminal, resp.HijackedResponse, idResp, user, workdir)
		if err != nil {
			return err
		}

		inspectResp, err := cr.cli.ExecInspect(ctx, idResp.ID, client.ExecInspectOptions{})
		if err != nil {
			return fmt.Errorf("failed to inspect exec: %w", err)
		}

		if inspectResp.ExitCode == 0 {
			return nil
		}
		return ExitCodeError(inspectResp.ExitCode)
	}
}

func (cr *containerReference) tryReadID(opt string, cbk func(id int)) common.Executor {
	return func(ctx context.Context) error {
		idResp, err := cr.cli.ExecCreate(ctx, cr.id, client.ExecCreateOptions{
			Cmd:          []string{"id", opt},
			AttachStdout: true,
			AttachStderr: true,
		})
		if err != nil {
			return nil
		}

		resp, err := cr.cli.ExecAttach(ctx, idResp.ID, client.ExecAttachOptions{})
		if err != nil {
			return nil
		}
		defer resp.Close()

		sid, err := resp.Reader.ReadString('\n')
		if err != nil {
			return nil
		}
		exp := regexp.MustCompile(`\d+\n`)
		found := exp.FindString(sid)
		id, err := strconv.ParseInt(strings.TrimSpace(found), 10, 32)
		if err != nil {
			return nil
		}
		cbk(int(id))

		return nil
	}
}

func (cr *containerReference) tryReadUID() common.Executor {
	return cr.tryReadID("-u", func(id int) { cr.UID = id })
}

func (cr *containerReference) tryReadGID() common.Executor {
	return cr.tryReadID("-g", func(id int) { cr.GID = id })
}

func (cr *containerReference) waitForCommand(ctx context.Context, isTerminal bool, resp client.HijackedResponse, _ client.ExecCreateResult, _, _ string) error {
	logger := common.Logger(ctx)

	cmdResponse := make(chan error)

	go func() {
		var outWriter io.Writer
		outWriter = cr.input.Stdout
		if outWriter == nil {
			outWriter = os.Stdout
		}
		errWriter := cr.input.Stderr
		if errWriter == nil {
			errWriter = os.Stderr
		}

		var err error
		if !isTerminal || os.Getenv("NORAW") != "" {
			_, err = stdcopy.StdCopy(outWriter, errWriter, resp.Reader)
		} else {
			_, err = io.Copy(outWriter, resp.Reader)
		}
		cmdResponse <- err
	}()

	select {
	case <-ctx.Done():
		// send ctrl + c
		_, err := resp.Conn.Write([]byte{3})
		if err != nil {
			logger.Warnf("Failed to send CTRL+C: %+s", err)
		}

		// we return the context canceled error to prevent other steps
		// from executing
		return ctx.Err()
	case err := <-cmdResponse:
		if err != nil {
			logger.Error(err)
		}

		return nil
	}
}

func (cr *containerReference) CopyTarStream(ctx context.Context, destPath string, tarStream io.Reader) error {
	// Mkdir
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	_ = tw.WriteHeader(&tar.Header{
		Name:     destPath,
		Mode:     0o777,
		Typeflag: tar.TypeDir,
	})
	tw.Close()
	_, err := cr.cli.CopyToContainer(ctx, cr.id, client.CopyToContainerOptions{
		DestinationPath: "/",
		Content:         buf,
	})
	if err != nil {
		return fmt.Errorf("failed to mkdir to copy content to container: %w", err)
	}
	// Copy Content
	_, err = cr.cli.CopyToContainer(ctx, cr.id, client.CopyToContainerOptions{
		DestinationPath: destPath,
		Content:         tarStream,
	})
	if err != nil {
		return fmt.Errorf("failed to copy content to container: %w", err)
	}
	// If this fails, then folders have wrong permissions on non root container
	if cr.UID != 0 || cr.GID != 0 {
		_ = cr.Exec([]string{"chown", "-R", fmt.Sprintf("%d:%d", cr.UID, cr.GID), destPath}, nil, "0", "")(ctx)
	}
	return nil
}

func (cr *containerReference) copyDir(dstPath, srcPath string, useGitIgnore bool) common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		tarFile, err := os.CreateTemp("", "act")
		if err != nil {
			return err
		}
		logger.Debugf("Writing tarball %s from %s", tarFile.Name(), srcPath)
		defer func(tarFile *os.File) {
			name := tarFile.Name()
			err := tarFile.Close()
			if !errors.Is(err, os.ErrClosed) {
				logger.Error(err)
			}
			err = os.Remove(name)
			if err != nil {
				logger.Error(err)
			}
		}(tarFile)
		tw := tar.NewWriter(tarFile)

		srcPrefix := filepath.Dir(srcPath)
		if !strings.HasSuffix(srcPrefix, string(filepath.Separator)) {
			srcPrefix += string(filepath.Separator)
		}
		logger.Debugf("Stripping prefix:%s src:%s", srcPrefix, srcPath)

		var ignorer gitignore.Matcher
		if useGitIgnore {
			ps, err := gitignore.ReadPatterns(polyfill.New(osfs.New(srcPath)), nil)
			if err != nil {
				logger.Debugf("Error loading .gitignore: %v", err)
			}

			ignorer = gitignore.NewMatcher(ps)
		}

		fc := &filecollector.FileCollector{
			Fs:        &filecollector.DefaultFs{},
			Ignorer:   ignorer,
			SrcPath:   srcPath,
			SrcPrefix: srcPrefix,
			Handler: &filecollector.TarCollector{
				TarWriter: tw,
				UID:       cr.UID,
				GID:       cr.GID,
				DstDir:    dstPath[1:],
			},
		}

		err = filepath.Walk(srcPath, fc.CollectFiles(ctx, []string{}))
		if err != nil {
			return err
		}
		if err := tw.Close(); err != nil {
			return err
		}

		logger.Debugf("Extracting content from '%s' to '%s'", tarFile.Name(), dstPath)
		_, err = tarFile.Seek(0, 0)
		if err != nil {
			return fmt.Errorf("failed to seek tar archive: %w", err)
		}
		_, err = cr.cli.CopyToContainer(ctx, cr.id, client.CopyToContainerOptions{
			DestinationPath: "/",
			Content:         tarFile,
		})
		if err != nil {
			return fmt.Errorf("failed to copy content to container: %w", err)
		}
		return nil
	}
}

func (cr *containerReference) copyContent(dstPath string, files ...*FileEntry) common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, file := range files {
			logger.Debugf("Writing entry to tarball %s len:%d", file.Name, len(file.Body))
			hdr := &tar.Header{
				Name: file.Name,
				Mode: file.Mode,
				Size: int64(len(file.Body)),
				Uid:  cr.UID,
				Gid:  cr.GID,
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if _, err := tw.Write([]byte(file.Body)); err != nil {
				return err
			}
		}
		if err := tw.Close(); err != nil {
			return err
		}

		logger.Debugf("Extracting content to '%s'", dstPath)
		_, err := cr.cli.CopyToContainer(ctx, cr.id, client.CopyToContainerOptions{
			DestinationPath: dstPath,
			Content:         &buf,
		})
		if err != nil {
			return fmt.Errorf("failed to copy content to container: %w", err)
		}
		return nil
	}
}

func (cr *containerReference) attach() common.Executor {
	return func(ctx context.Context) error {
		out, err := cr.cli.ContainerAttach(ctx, cr.id, client.ContainerAttachOptions{
			Stream: true,
			Stdout: true,
			Stderr: true,
		})
		if err != nil {
			return fmt.Errorf("failed to attach to container: %w", err)
		}
		isTerminal := cr.input.AllocatePTY

		var outWriter io.Writer
		outWriter = cr.input.Stdout
		if outWriter == nil {
			outWriter = os.Stdout
		}
		errWriter := cr.input.Stderr
		if errWriter == nil {
			errWriter = os.Stderr
		}
		go func() {
			if !isTerminal || os.Getenv("NORAW") != "" {
				_, err = stdcopy.StdCopy(outWriter, errWriter, out.Reader)
			} else {
				_, err = io.Copy(outWriter, out.Reader)
			}
			if err != nil {
				common.Logger(ctx).Error(err)
			}
		}()
		return nil
	}
}

func (cr *containerReference) start() common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		logger.Debugf("Starting container: %v", cr.id)

		if _, err := cr.cli.ContainerStart(ctx, cr.id, client.ContainerStartOptions{}); err != nil {
			return fmt.Errorf("failed to start container: %w", err)
		}

		logger.Debugf("Started container: %v", cr.id)
		return nil
	}
}

func (cr *containerReference) wait() common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		waitResult := cr.cli.ContainerWait(ctx, cr.id, client.ContainerWaitOptions{
			Condition: container.WaitConditionNotRunning,
		})
		var statusCode int64
		select {
		case err := <-waitResult.Error:
			if err != nil {
				return fmt.Errorf("failed to wait for container: %w", err)
			}
		case status := <-waitResult.Result:
			statusCode = status.StatusCode
		}

		logger.Debugf("Return status: %v", statusCode)

		if statusCode == 0 {
			return nil
		}

		return ExitCodeError(statusCode)
	}
}

// For Gitea
// sanitizeConfig remove the invalid configurations from `config` and `hostConfig`
func (cr *containerReference) sanitizeConfig(ctx context.Context, config *container.Config, hostConfig *container.HostConfig) (*container.Config, *container.HostConfig) {
	logger := common.Logger(ctx)

	if len(cr.input.ValidVolumes) > 0 {
		matcher := newValidVolumeMatcher(ctx, cr.input.ValidVolumes)
		// sanitize binds
		sanitizedBinds := make([]string, 0, len(hostConfig.Binds))
		for _, bind := range hostConfig.Binds {
			parsed, err := loader.ParseVolume(bind)
			if err != nil {
				logger.Warnf("parse volume [%s] error: %v", bind, err)
				continue
			}
			if parsed.Source == "" {
				// anonymous volume
				sanitizedBinds = append(sanitizedBinds, bind)
				continue
			}
			if matcher.isValid(parsed.Source, mount.Type(parsed.Type)) {
				sanitizedBinds = append(sanitizedBinds, bind)
			} else {
				logger.Warnf("[%s] is not a valid volume, will be ignored", parsed.Source)
			}
		}
		hostConfig.Binds = sanitizedBinds
		// sanitize mounts
		sanitizedMounts := make([]mount.Mount, 0, len(hostConfig.Mounts))
		for _, mt := range hostConfig.Mounts {
			if matcher.isValid(mt.Source, mt.Type) {
				sanitizedMounts = append(sanitizedMounts, mt)
			} else {
				logger.Warnf("[%s] is not a valid volume, will be ignored", mt.Source)
			}
		}
		hostConfig.Mounts = sanitizedMounts
	} else {
		hostConfig.Binds = []string{}
		hostConfig.Mounts = []mount.Mount{}
	}

	return config, hostConfig
}

type validVolumeMatcher struct {
	allowAll bool
	named    []glob.Glob
	host     []glob.Glob
}

func newValidVolumeMatcher(ctx context.Context, validVolumes []string) validVolumeMatcher {
	logger := common.Logger(ctx)
	ret := validVolumeMatcher{
		named: make([]glob.Glob, 0, len(validVolumes)),
		host:  make([]glob.Glob, 0, len(validVolumes)),
	}

	for _, v := range validVolumes {
		if v == "**" {
			ret.allowAll = true
			continue
		}
		if !isHostVolumePattern(v) {
			if g, err := glob.Compile(v); err != nil {
				logger.Errorf("create glob from %s error: %v", v, err)
			} else {
				ret.named = append(ret.named, g)
			}
			continue
		}
		normalized, err := normalizeHostVolumePath(v)
		if err != nil {
			logger.Errorf("normalize volume pattern %s error: %v", v, err)
			continue
		}
		if g, err := glob.Compile(normalized); err != nil {
			logger.Errorf("create glob from %s error: %v", normalized, err)
		} else {
			ret.host = append(ret.host, g)
		}
	}

	return ret
}

func (m validVolumeMatcher) isValid(source string, sourceType mount.Type) bool {
	if m.allowAll {
		return true
	}
	if isHostVolumeSource(source, sourceType) {
		normalized, err := normalizeHostVolumePath(source)
		if err != nil {
			return false
		}
		for _, g := range m.host {
			if g.Match(normalized) {
				return true
			}
		}
		return false
	}
	for _, g := range m.named {
		if g.Match(source) {
			return true
		}
	}
	return false
}

func isHostVolumePattern(pattern string) bool {
	return filepath.IsAbs(pattern) ||
		strings.HasPrefix(pattern, "."+string(filepath.Separator)) ||
		strings.HasPrefix(pattern, ".."+string(filepath.Separator)) ||
		strings.Contains(pattern, "/") ||
		strings.Contains(pattern, `\`)
}

func isHostVolumeSource(source string, sourceType mount.Type) bool {
	if sourceType == mount.TypeBind {
		return true
	}
	if sourceType == mount.TypeVolume {
		return false
	}
	return isHostVolumePattern(source)
}

func normalizeHostVolumePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return evalSymlinksExistingPrefix(abs)
}

func evalSymlinksExistingPrefix(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	current := path
	var missing []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for _, name := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, name)
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(path), nil
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
