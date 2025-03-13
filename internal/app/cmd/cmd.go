// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"gitea.com/gitea/act_runner/internal/pkg/config"
	"gitea.com/gitea/act_runner/internal/pkg/ver"
)

func Execute(ctx context.Context) {
	// ./act_runner
	rootCmd := &cobra.Command{
		Use:          "act_runner [event name to run]\nIf no event name passed, will default to \"on: push\"",
		Short:        "Run GitHub actions locally by specifying the event name (e.g. `push`) or an action name directly.",
		Args:         cobra.MaximumNArgs(1),
		Version:      ver.Version(),
		SilenceUsage: true,
	}
	configFile := ""
	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "", "Config file path")

	// ./act_runner register
	var regArgs registerArgs
	registerCmd := &cobra.Command{
		Use:   "register",
		Short: "Register a runner to the server",
		Args:  cobra.MaximumNArgs(0),
		RunE:  runRegister(ctx, &regArgs, &configFile), // must use a pointer to regArgs
	}
	registerCmd.Flags().BoolVar(&regArgs.NoInteractive, "no-interactive", false, "Disable interactive mode")
	registerCmd.Flags().StringVar(&regArgs.InstanceAddr, "instance", "", "Gitea instance address")
	registerCmd.Flags().StringVar(&regArgs.Token, "token", "", "Runner token")
	registerCmd.Flags().StringVar(&regArgs.RunnerName, "name", "", "Runner name")
	registerCmd.Flags().StringVar(&regArgs.Labels, "labels", "", "Runner tags, comma separated")
	registerCmd.Flags().BoolVar(&regArgs.Ephemeral, "ephemeral", false, "Configure the runner to be ephemeral and only ever be able to pick a single job (stricter than --once)")
	rootCmd.AddCommand(registerCmd)

	// ./act_runner daemon
	var daemArgs daemonArgs
	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run as a runner daemon",
		Args:  cobra.MaximumNArgs(0),
		RunE:  runDaemon(ctx, &daemArgs, &configFile),
	}
	daemonCmd.Flags().BoolVar(&daemArgs.Once, "once", false, "Run one job then exit")
	rootCmd.AddCommand(daemonCmd)

	// ./act_runner exec
	rootCmd.AddCommand(loadExecCmd(ctx))

	// ./act_runner config
	rootCmd.AddCommand(&cobra.Command{
		Use:   "generate-config",
		Short: "Generate an example config file",
		Args:  cobra.MaximumNArgs(0),
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("%s", config.Example)
		},
	})

	// ./act_runner cache-server
	var cacheArgs cacheServerArgs
	cacheCmd := &cobra.Command{
		Use:   "cache-server",
		Short: "Start a cache server for the cache action",
		Args:  cobra.MaximumNArgs(0),
		RunE:  runCacheServer(ctx, &configFile, &cacheArgs),
	}
	cacheCmd.Flags().StringVarP(&cacheArgs.Dir, "dir", "d", "", "Cache directory")
	cacheCmd.Flags().StringVarP(&cacheArgs.Host, "host", "s", "", "Host of the cache server")
	cacheCmd.Flags().Uint16VarP(&cacheArgs.Port, "port", "p", 0, "Port of the cache server")
	rootCmd.AddCommand(cacheCmd)

	// hide completion command
	rootCmd.CompletionOptions.HiddenDefaultCmd = true

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
