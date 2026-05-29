// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !(WITHOUT_DOCKER || !(linux || darwin || windows || netbsd))

package container

import (
	"context"

	"gitea.com/gitea/runner/act/common"

	"github.com/moby/moby/client"
)

func NewDockerVolumeRemoveExecutor(volumeName string, force bool) common.Executor {
	return func(ctx context.Context) error {
		cli, err := GetDockerClient(ctx)
		if err != nil {
			return err
		}
		defer cli.Close()

		list, err := cli.VolumeList(ctx, client.VolumeListOptions{})
		if err != nil {
			return err
		}

		for _, vol := range list.Items {
			if vol.Name == volumeName {
				return removeExecutor(volumeName, force)(ctx)
			}
		}

		// Volume not found - do nothing
		return nil
	}
}

func removeExecutor(volume string, force bool) common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		logger.Debugf("docker volume rm %s", volume)

		if common.Dryrun(ctx) {
			return nil
		}

		cli, err := GetDockerClient(ctx)
		if err != nil {
			return err
		}
		defer cli.Close()

		_, err = cli.VolumeRemove(ctx, volume, client.VolumeRemoveOptions{Force: force})
		return err
	}
}
