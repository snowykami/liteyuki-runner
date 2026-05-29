// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import "context"

type ExecutionsEnvironment interface {
	Container
	ToContainerPath(string) string
	GetActPath() string
	GetPathVariableName() string
	DefaultPathVariable() string
	JoinPathVariable(...string) string
	GetRunnerContext(ctx context.Context) map[string]any
	// On windows PATH and Path are the same key
	IsEnvironmentCaseInsensitive() bool
}
