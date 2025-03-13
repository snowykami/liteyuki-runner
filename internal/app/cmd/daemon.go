// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/mattn/go-isatty"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"gitea.com/gitea/act_runner/internal/app/poll"
	"gitea.com/gitea/act_runner/internal/app/run"
	"gitea.com/gitea/act_runner/internal/pkg/client"
	"gitea.com/gitea/act_runner/internal/pkg/config"
	"gitea.com/gitea/act_runner/internal/pkg/envcheck"
	"gitea.com/gitea/act_runner/internal/pkg/labels"
	"gitea.com/gitea/act_runner/internal/pkg/ver"
)

func runDaemon(ctx context.Context, daemArgs *daemonArgs, configFile *string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadDefault(*configFile)
		if err != nil {
			return fmt.Errorf("invalid configuration: %w", err)
		}

		initLogging(cfg)
		log.Infoln("Starting runner daemon")

		reg, err := config.LoadRegistration(cfg.Runner.File)
		if os.IsNotExist(err) {
			log.Error("registration file not found, please register the runner first")
			return err
		} else if err != nil {
			return fmt.Errorf("failed to load registration file: %w", err)
		}

		lbls := reg.Labels
		if len(cfg.Runner.Labels) > 0 {
			lbls = cfg.Runner.Labels
		}

		ls := labels.Labels{}
		for _, l := range lbls {
			label, err := labels.Parse(l)
			if err != nil {
				log.WithError(err).Warnf("ignored invalid label %q", l)
				continue
			}
			ls = append(ls, label)
		}
		if len(ls) == 0 {
			log.Warn("no labels configured, runner may not be able to pick up jobs")
		}

		if ls.RequireDocker() {
			dockerSocketPath, err := getDockerSocketPath(cfg.Container.DockerHost)
			if err != nil {
				return err
			}
			if err := envcheck.CheckIfDockerRunning(ctx, dockerSocketPath); err != nil {
				return err
			}
			// if dockerSocketPath passes the check, override DOCKER_HOST with dockerSocketPath
			os.Setenv("DOCKER_HOST", dockerSocketPath)
			// empty cfg.Container.DockerHost means act_runner need to find an available docker host automatically
			// and assign the path to cfg.Container.DockerHost
			if cfg.Container.DockerHost == "" {
				cfg.Container.DockerHost = dockerSocketPath
			}
			// check the scheme, if the scheme is not npipe or unix
			// set cfg.Container.DockerHost to "-" because it can't be mounted to the job container
			if protoIndex := strings.Index(cfg.Container.DockerHost, "://"); protoIndex != -1 {
				scheme := cfg.Container.DockerHost[:protoIndex]
				if !strings.EqualFold(scheme, "npipe") && !strings.EqualFold(scheme, "unix") {
					cfg.Container.DockerHost = "-"
				}
			}
		}

		if !slices.Equal(reg.Labels, ls.ToStrings()) {
			reg.Labels = ls.ToStrings()
			if err := config.SaveRegistration(cfg.Runner.File, reg); err != nil {
				return fmt.Errorf("failed to save runner config: %w", err)
			}
			log.Infof("labels updated to: %v", reg.Labels)
		}

		cli := client.New(
			reg.Address,
			cfg.Runner.Insecure,
			reg.UUID,
			reg.Token,
			ver.Version(),
		)

		runner := run.NewRunner(cfg, reg, cli)

		// declare the labels of the runner before fetching tasks
		resp, err := runner.Declare(ctx, ls.Names())
		if err != nil && connect.CodeOf(err) == connect.CodeUnimplemented {
			log.Errorf("Your Gitea version is too old to support runner declare, please upgrade to v1.21 or later")
			return err
		} else if err != nil {
			log.WithError(err).Error("fail to invoke Declare")
			return err
		} else {
			log.Infof("runner: %s, with version: %s, with labels: %v, declare successfully",
				resp.Msg.Runner.Name, resp.Msg.Runner.Version, resp.Msg.Runner.Labels)
		}

		poller := poll.New(cfg, cli, runner)

		if daemArgs.Once || reg.Ephemeral {
			done := make(chan struct{})
			go func() {
				defer close(done)
				poller.PollOnce()
			}()

			// shutdown when we complete a job or cancel is requested
			select {
			case <-ctx.Done():
			case <-done:
			}
		} else {
			go poller.Poll()

			<-ctx.Done()
		}

		log.Infof("runner: %s shutdown initiated, waiting %s for running jobs to complete before shutting down", resp.Msg.Runner.Name, cfg.Runner.ShutdownTimeout)

		ctx, cancel := context.WithTimeout(context.Background(), cfg.Runner.ShutdownTimeout)
		defer cancel()

		err = poller.Shutdown(ctx)
		if err != nil {
			log.Warnf("runner: %s cancelled in progress jobs during shutdown", resp.Msg.Runner.Name)
		}

		return nil
	}
}

type daemonArgs struct {
	Once bool
}

// initLogging setup the global logrus logger.
func initLogging(cfg *config.Config) {
	isTerm := isatty.IsTerminal(os.Stdout.Fd())
	format := &log.TextFormatter{
		DisableColors: !isTerm,
		FullTimestamp: true,
	}
	log.SetFormatter(format)

	if l := cfg.Log.Level; l != "" {
		level, err := log.ParseLevel(l)
		if err != nil {
			log.WithError(err).
				Errorf("invalid log level: %q", l)
		}

		// debug level
		if level == log.DebugLevel {
			log.SetReportCaller(true)
			format.CallerPrettyfier = func(f *runtime.Frame) (string, string) {
				// get function name
				s := strings.Split(f.Function, ".")
				funcname := "[" + s[len(s)-1] + "]"
				// get file name and line number
				_, filename := path.Split(f.File)
				filename = "[" + filename + ":" + strconv.Itoa(f.Line) + "]"
				return funcname, filename
			}
			log.SetFormatter(format)
		}

		if log.GetLevel() != level {
			log.Infof("log level changed to %v", level)
			log.SetLevel(level)
		}
	}
}

var commonSocketPaths = []string{
	"/var/run/docker.sock",
	"/run/podman/podman.sock",
	"$HOME/.colima/docker.sock",
	"$XDG_RUNTIME_DIR/docker.sock",
	"$XDG_RUNTIME_DIR/podman/podman.sock",
	`\\.\pipe\docker_engine`,
	"$HOME/.docker/run/docker.sock",
}

func getDockerSocketPath(configDockerHost string) (string, error) {
	// a `-` means don't mount the docker socket to job containers
	if configDockerHost != "" && configDockerHost != "-" {
		return configDockerHost, nil
	}

	socket, found := os.LookupEnv("DOCKER_HOST")
	if found {
		return socket, nil
	}

	for _, p := range commonSocketPaths {
		if _, err := os.Lstat(os.ExpandEnv(p)); err == nil {
			if strings.HasPrefix(p, `\\.\`) {
				return "npipe://" + filepath.ToSlash(os.ExpandEnv(p)), nil
			}
			return "unix://" + filepath.ToSlash(os.ExpandEnv(p)), nil
		}
	}

	return "", fmt.Errorf("daemon Docker Engine socket not found and docker_host config was invalid")
}
