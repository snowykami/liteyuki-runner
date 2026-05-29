// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2023 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !(WITHOUT_DOCKER || !(linux || darwin || windows || netbsd))

package container

import (
	"context"

	"gitea.com/gitea/runner/act/common"

	"github.com/moby/moby/client"
)

func NewDockerNetworkCreateExecutor(name string) common.Executor {
	return func(ctx context.Context) error {
		cli, err := GetDockerClient(ctx)
		if err != nil {
			return err
		}
		defer cli.Close()

		// Only create the network if it doesn't exist
		networks, err := cli.NetworkList(ctx, client.NetworkListOptions{})
		if err != nil {
			return err
		}
		// For Gitea, reduce log noise
		// common.Logger(ctx).Debugf("%v", networks)
		for _, n := range networks.Items {
			if n.Name == name {
				common.Logger(ctx).Debugf("Network %v exists", name)
				return nil
			}
		}

		_, err = cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
			Driver: "bridge",
			Scope:  "local",
		})
		if err != nil {
			return err
		}

		return nil
	}
}

func NewDockerNetworkRemoveExecutor(name string) common.Executor {
	return func(ctx context.Context) error {
		cli, err := GetDockerClient(ctx)
		if err != nil {
			return err
		}
		defer cli.Close()

		// Make sure that all network of the specified name are removed
		// cli.NetworkRemove refuses to remove a network if there are duplicates
		networks, err := cli.NetworkList(ctx, client.NetworkListOptions{})
		if err != nil {
			return err
		}
		// For Gitea, reduce log noise
		// common.Logger(ctx).Debugf("%v", networks)
		for _, n := range networks.Items {
			if n.Name == name {
				result, err := cli.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
				if err != nil {
					return err
				}

				if len(result.Network.Containers) == 0 {
					if _, err = cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil {
						common.Logger(ctx).Debugf("%v", err)
					}
				} else {
					common.Logger(ctx).Debugf("Refusing to remove network %v because it still has active endpoints", name)
				}
			}
		}

		return err
	}
}
