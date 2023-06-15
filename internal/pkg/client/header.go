// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package client

const (
	UUIDHeader  = "x-runner-uuid"
	TokenHeader = "x-runner-token"
	// Deprecated: could be removed after Gitea 1.20 released
	VersionHeader = "x-runner-version"
)
