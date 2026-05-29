// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	log.SetLevel(log.DebugLevel)
}

// buildScratchImage builds a tiny empty image for the given platform locally (FROM scratch, no
// network or emulation since there is nothing to run) and returns its tag, removing it after
// the test.
func buildScratchImage(t *testing.T, platform string) string {
	t.Helper()
	tag := fmt.Sprintf("act-test-exists-%s:latest", strings.TrimPrefix(platform, "linux/"))
	cmd := exec.Command("docker", "build", "--platform", platform, "-t", tag, "-")
	cmd.Stdin = strings.NewReader("FROM scratch\nLABEL act-test=1\n")
	// Force BuildKit: it records the requested architecture in the image config for a
	// FROM-scratch build, whereas the classic builder ignores --platform and tags it with the
	// host arch, which would break the per-platform existence assertions below.
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
	return tag
}

func TestImageExistsLocally(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	// a non-existent image is reported absent
	missing, err := ImageExistsLocally(ctx, "library/alpine:this-random-tag-will-never-exist", "linux/amd64")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.False(t, missing)

	// Build tiny images for two architectures locally so per-platform existence can be checked
	// offline (formerly pulled node:24-bookworm-slim for amd64 and arm64 over the network).
	amd64Ref := buildScratchImage(t, "linux/amd64")
	arm64Ref := buildScratchImage(t, "linux/arm64")

	amd64Exists, err := ImageExistsLocally(ctx, amd64Ref, "linux/amd64")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.True(t, amd64Exists)

	// a non-host architecture image is detected for its own architecture
	arm64Exists, err := ImageExistsLocally(ctx, arm64Ref, "linux/arm64")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.True(t, arm64Exists)

	// a present image is reported absent for a different platform
	wrongPlatform, err := ImageExistsLocally(ctx, amd64Ref, "linux/arm64")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.False(t, wrongPlatform)
}
