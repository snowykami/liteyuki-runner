// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"context"
	"testing"

	mobyclient "github.com/moby/moby/client"
)

// requireDocker skips the test unless a reachable docker daemon is available.
// GetDockerClient succeeds even without a running daemon (its ping is best-effort),
// so the daemon has to be pinged explicitly here to decide whether to skip.
func requireDocker(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	cli, err := GetDockerClient(ctx)
	if err != nil {
		t.Skipf("skipping: docker client unavailable: %v", err)
	}
	defer cli.Close()
	if _, err := cli.Ping(ctx, mobyclient.PingOptions{}); err != nil {
		t.Skipf("skipping: docker daemon unreachable: %v", err)
	}
}
