// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package envcheck

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"
)

func CheckIfDockerRunning(ctx context.Context, configDockerHost string) error {
	opts := []client.Opt{
		client.FromEnv,
	}

	if configDockerHost != "" {
		opts = append(opts, client.WithHost(configDockerHost))
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return err
	}
	defer cli.Close()

	_, err = cli.Ping(ctx)
	if err != nil {
		return fmt.Errorf("cannot ping the docker daemon, is it running? %w", err)
	}

	return nil
}
