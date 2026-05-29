// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestHostEnv(t *testing.T) (*HostEnvironment, string) {
	t.Helper()
	e := &HostEnvironment{Path: t.TempDir()}
	return e, filepath.Join(e.Path, "envfile")
}

func TestParseEnvFileSingleLine(t *testing.T) {
	e, envPath := newTestHostEnv(t)
	require.NoError(t, os.WriteFile(envPath, []byte("FOO=bar\nBAZ=qux\n"), 0o600))

	env := map[string]string{}
	require.NoError(t, parseEnvFile(e, envPath, &env)(context.Background()))
	assert.Equal(t, "bar", env["FOO"])
	assert.Equal(t, "qux", env["BAZ"])
}

func TestParseEnvFileMultiLine(t *testing.T) {
	e, envPath := newTestHostEnv(t)
	content := "FOO<<EOF\nline1\nline2\nEOF\n"
	require.NoError(t, os.WriteFile(envPath, []byte(content), 0o600))

	env := map[string]string{}
	require.NoError(t, parseEnvFile(e, envPath, &env)(context.Background()))
	assert.Equal(t, "line1\nline2", env["FOO"])
}

func TestParseEnvFileLargeValueWithinLimit(t *testing.T) {
	e, envPath := newTestHostEnv(t)
	big := strings.Repeat("x", 2*1024*1024)
	content := "FOO<<EOF\n" + big + "\nEOF\n"
	require.NoError(t, os.WriteFile(envPath, []byte(content), 0o600))

	env := map[string]string{}
	require.NoError(t, parseEnvFile(e, envPath, &env)(context.Background()))
	assert.Equal(t, big, env["FOO"])
}

func TestParseEnvFileLineExceedsBufferReportsScannerError(t *testing.T) {
	e, envPath := newTestHostEnv(t)
	tooBig := strings.Repeat("x", 17*1024*1024) // over the 16 MiB cap
	content := "FOO<<EOF\n" + tooBig + "\nEOF\n"
	require.NoError(t, os.WriteFile(envPath, []byte(content), 0o600))

	env := map[string]string{}
	err := parseEnvFile(e, envPath, &env)(context.Background())
	require.ErrorIs(t, err, bufio.ErrTooLong)
	assert.Contains(t, err.Error(), "reading env file")
}

func TestParseEnvFileMissingDelimiter(t *testing.T) {
	e, envPath := newTestHostEnv(t)
	require.NoError(t, os.WriteFile(envPath, []byte("FOO<<EOF\nline1\nline2\n"), 0o600))

	env := map[string]string{}
	err := parseEnvFile(e, envPath, &env)(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delimiter")
}
