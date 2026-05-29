// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2021 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !(WITHOUT_DOCKER || !(linux || darwin || windows || netbsd))

package container

import (
	"context"

	"gitea.com/gitea/runner/act/common"

	"github.com/distribution/reference"
	"github.com/docker/cli/cli/config"
	"github.com/moby/moby/api/types/registry"
)

func LoadDockerAuthConfig(ctx context.Context, image string) (registry.AuthConfig, error) {
	logger := common.Logger(ctx)
	// config.LoadDefaultConfigFile panics on nil io.Writer when the config
	// file is malformed; use config.Load to route errors through the logger.
	cfg, err := config.Load(config.Dir())
	if err != nil {
		logger.Warnf("Could not load docker config: %v", err)
		return registry.AuthConfig{}, err
	}
	registryKey := registryAuthConfigKey("docker.io")
	if image != "" {
		if registryRef, refErr := reference.ParseNormalizedNamed(image); refErr != nil {
			logger.Warnf("Could not normalize image reference: %v", refErr)
		} else {
			registryKey = registryAuthConfigKey(reference.Domain(registryRef))
		}
	}

	authConfig, err := cfg.GetAuthConfig(registryKey)
	if err != nil {
		logger.Warnf("Could not get auth config from docker config: %v", err)
		return registry.AuthConfig{}, err
	}

	return registry.AuthConfig(authConfig), nil
}

func LoadDockerAuthConfigs(ctx context.Context) map[string]registry.AuthConfig {
	logger := common.Logger(ctx)
	cfg, err := config.Load(config.Dir())
	if err != nil {
		logger.Warnf("Could not load docker config: %v", err)
		return nil
	}
	creds, err := cfg.GetAllCredentials()
	if err != nil {
		logger.Warnf("Could not get docker auth configs: %v", err)
		return nil
	}
	authConfigs := make(map[string]registry.AuthConfig, len(creds))
	for k, v := range creds {
		authConfigs[k] = registry.AuthConfig(v)
	}

	return authConfigs
}

func registryAuthConfigKey(domainName string) string {
	if domainName == "docker.io" || domainName == "index.docker.io" {
		return "https://index.docker.io/v1/"
	}
	return domainName
}
