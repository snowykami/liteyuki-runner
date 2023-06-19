// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package envcheck

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"

	"gitea.com/gitea/act_runner/internal/pkg/config"
)

func CheckIfDockerRunning(ctx context.Context, cfg *config.Config) error {
	opts := []client.Opt{
		client.FromEnv,
	}

	if cfg.Container.DockerHost != "" {
		opts = append(opts, client.WithHost(cfg.Container.DockerHost))
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return err
	}
	defer cli.Close()

	_, err = cli.Ping(ctx)
	if err != nil {
		return fmt.Errorf("cannot ping the docker daemon, does it running? %w", err)
	}

	return nil
}
