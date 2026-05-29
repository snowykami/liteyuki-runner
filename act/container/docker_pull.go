// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !(WITHOUT_DOCKER || !(linux || darwin || windows || netbsd))

package container

import (
	"context"
	"fmt"
	"strings"

	"gitea.com/gitea/runner/act/common"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

// NewDockerPullExecutor function to create a run executor for the container
func NewDockerPullExecutor(input NewDockerPullExecutorInput) common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		logger.Debugf("docker pull %v", input.Image)

		if common.Dryrun(ctx) {
			return nil
		}

		pull := input.ForcePull
		if !pull {
			imageExists, err := ImageExistsLocally(ctx, input.Image, input.Platform)
			logger.Debugf("Image exists? %v", imageExists)
			if err != nil {
				return fmt.Errorf("unable to determine if image already exists for image '%s' (%s): %w", input.Image, input.Platform, err)
			}

			if !imageExists {
				pull = true
			}
		}

		if !pull {
			return nil
		}

		imageRef := cleanImage(ctx, input.Image)
		logger.Debugf("pulling image '%v' (%s)", imageRef, input.Platform)

		cli, err := GetDockerClient(ctx)
		if err != nil {
			return err
		}
		defer cli.Close()

		imagePullOptions, err := getImagePullOptions(ctx, input)
		if err != nil {
			return err
		}

		reader, err := cli.ImagePull(ctx, imageRef, imagePullOptions)

		_ = logDockerResponse(logger, reader, err != nil)
		if err != nil {
			if imagePullOptions.RegistryAuth != "" && strings.Contains(err.Error(), "unauthorized") {
				logger.Errorf("pulling image '%v' (%s) failed with credentials %s retrying without them, please check for stale docker config files", imageRef, input.Platform, err.Error())
				imagePullOptions.RegistryAuth = ""
				reader, err = cli.ImagePull(ctx, imageRef, imagePullOptions)

				_ = logDockerResponse(logger, reader, err != nil)
			}
			return err
		}
		return nil
	}
}

func getImagePullOptions(ctx context.Context, input NewDockerPullExecutorInput) (client.ImagePullOptions, error) {
	imagePullOptions := client.ImagePullOptions{}
	platform, err := parsePlatform(input.Platform)
	if err != nil {
		return imagePullOptions, err
	}
	if platform != nil {
		imagePullOptions.Platforms = []specs.Platform{*platform}
	}
	logger := common.Logger(ctx)

	if input.Username != "" && input.Password != "" {
		logger.Debugf("using authentication for docker pull")

		encodedAuth, err := authconfig.Encode(registry.AuthConfig{
			Username: input.Username,
			Password: input.Password,
		})
		if err != nil {
			return imagePullOptions, err
		}

		imagePullOptions.RegistryAuth = encodedAuth
	} else {
		authConfig, err := LoadDockerAuthConfig(ctx, input.Image)
		if err != nil {
			return imagePullOptions, err
		}
		if authConfig.Username == "" && authConfig.Password == "" {
			return imagePullOptions, nil
		}
		logger.Info("using DockerAuthConfig authentication for docker pull")

		imagePullOptions.RegistryAuth, err = authconfig.Encode(authConfig)
		if err != nil {
			return imagePullOptions, err
		}
	}

	return imagePullOptions, nil
}

func cleanImage(ctx context.Context, imageName string) string {
	ref, err := reference.ParseAnyReference(imageName)
	if err != nil {
		common.Logger(ctx).Error(err)
		return ""
	}

	return ref.String()
}
