// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"fmt"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"

	"github.com/bufbuild/connect-go"
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

func runDaemon(ctx context.Context, configFile *string) func(cmd *cobra.Command, args []string) error {
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
			if err := envcheck.CheckIfDockerRunning(ctx); err != nil {
				return err
			}
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
			// Gitea instance is older version. skip declare step.
			log.Warn("Because the Gitea instance is an old version, skip declare labels and version.")
		} else if err != nil {
			log.WithError(err).Error("fail to invoke Declare")
			return err
		} else {
			log.Infof("runner: %s, with version: %s, with labels: %v, declare successfully",
				resp.Msg.Runner.Name, resp.Msg.Runner.Version, resp.Msg.Runner.Labels)
			// if declare successfully, override the labels in the.runner file with valid labels in the config file (if specified)
			reg.Labels = ls.ToStrings()
			if err := config.SaveRegistration(cfg.Runner.File, reg); err != nil {
				return fmt.Errorf("failed to save runner config: %w", err)
			}
		}

		poller := poll.New(cfg, cli, runner)

		poller.Poll(ctx)

		return nil
	}
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
