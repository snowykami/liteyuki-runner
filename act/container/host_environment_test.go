// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gitea.com/gitea/runner/act/common"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Type assert HostEnvironment implements ExecutionsEnvironment
var _ ExecutionsEnvironment = &HostEnvironment{}

func TestCopyDir(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	e := &HostEnvironment{
		Path:      filepath.Join(dir, "path"),
		TmpDir:    filepath.Join(dir, "tmp"),
		ToolCache: filepath.Join(dir, "tool_cache"),
		ActPath:   filepath.Join(dir, "act_path"),
		StdOut:    os.Stdout,
		Workdir:   path.Join("testdata", "scratch"),
	}
	_ = os.MkdirAll(e.Path, 0o700)
	_ = os.MkdirAll(e.TmpDir, 0o700)
	_ = os.MkdirAll(e.ToolCache, 0o700)
	_ = os.MkdirAll(e.ActPath, 0o700)
	err := e.CopyDir(e.Workdir, e.Path, true)(ctx)
	assert.NoError(t, err)
}

func TestGetContainerArchive(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	e := &HostEnvironment{
		Path:      filepath.Join(dir, "path"),
		TmpDir:    filepath.Join(dir, "tmp"),
		ToolCache: filepath.Join(dir, "tool_cache"),
		ActPath:   filepath.Join(dir, "act_path"),
		StdOut:    os.Stdout,
		Workdir:   path.Join("testdata", "scratch"),
	}
	_ = os.MkdirAll(e.Path, 0o700)
	_ = os.MkdirAll(e.TmpDir, 0o700)
	_ = os.MkdirAll(e.ToolCache, 0o700)
	_ = os.MkdirAll(e.ActPath, 0o700)
	expectedContent := []byte("sdde/7sh")
	err := os.WriteFile(filepath.Join(e.Path, "action.yml"), expectedContent, 0o600)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	archive, err := e.GetContainerArchive(ctx, e.Path)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	defer archive.Close()
	reader := tar.NewReader(archive)
	h, err := reader.Next()
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, "action.yml", h.Name)
	content, err := io.ReadAll(reader)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, expectedContent, content)
	_, err = reader.Next()
	assert.ErrorIs(t, err, io.EOF)
}

func TestHostEnvironmentExecExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	dir := t.TempDir()
	ctx := context.Background()
	e := &HostEnvironment{
		Path:      filepath.Join(dir, "path"),
		TmpDir:    filepath.Join(dir, "tmp"),
		ToolCache: filepath.Join(dir, "tool_cache"),
		ActPath:   filepath.Join(dir, "act_path"),
		StdOut:    io.Discard,
		Workdir:   filepath.Join(dir, "path"),
	}
	for _, p := range []string{e.Path, e.TmpDir, e.ToolCache, e.ActPath} {
		assert.NoError(t, os.MkdirAll(p, 0o700)) //nolint:testifylint // test setup
	}

	err := e.Exec([]string{"sh", "-c", "exit 3"}, map[string]string{"PATH": os.Getenv("PATH")}, "", "")(ctx)
	var exitErr ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, ExitCodeError(3), exitErr)
	assert.Equal(t, "Process completed with exit code 3.", err.Error())
}

func TestHostEnvironmentAllocatePTY(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	for _, tc := range []struct {
		name     string
		allocPTY bool
		expect   string
	}{
		{name: "off", allocPTY: false, expect: "NOTTY"},
		{name: "on", allocPTY: true, expect: "TTY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			buf := &bytes.Buffer{}
			e := &HostEnvironment{
				Path:        filepath.Join(dir, "path"),
				TmpDir:      filepath.Join(dir, "tmp"),
				ToolCache:   filepath.Join(dir, "tool_cache"),
				ActPath:     filepath.Join(dir, "act_path"),
				StdOut:      buf,
				Workdir:     filepath.Join(dir, "path"),
				AllocatePTY: tc.allocPTY,
			}
			for _, p := range []string{e.Path, e.TmpDir, e.ToolCache, e.ActPath} {
				require.NoError(t, os.MkdirAll(p, 0o700))
			}

			err := e.Exec(
				[]string{"sh", "-c", "[ -t 1 ] && printf TTY || printf NOTTY"},
				map[string]string{"PATH": os.Getenv("PATH")}, "", "",
			)(context.Background())
			require.NoError(t, err)
			got := strings.TrimSpace(strings.ReplaceAll(buf.String(), "\r", ""))
			assert.Equal(t, tc.expect, got)
		})
	}
}

func TestHostEnvironmentRemovePreservesWorkdirByDefault(t *testing.T) {
	logger := logrus.New()
	ctx := common.WithLogger(context.Background(), logrus.NewEntry(logger))
	base := t.TempDir()
	miscRoot := filepath.Join(base, "misc")
	path := filepath.Join(miscRoot, "hostexecutor")
	require.NoError(t, os.MkdirAll(path, 0o700))
	workdir := filepath.Join(base, "workspace", "owner", "repo")
	require.NoError(t, os.MkdirAll(workdir, 0o700))

	e := &HostEnvironment{
		Path:    path,
		Workdir: workdir,
		CleanUp: func() {
			_ = os.RemoveAll(miscRoot)
		},
		StdOut: os.Stdout,
	}
	require.NoError(t, e.Remove()(ctx))
	_, err := os.Stat(workdir)
	require.NoError(t, err)
}

func TestHostEnvironmentRemoveCleansWorkdirWhenOwned(t *testing.T) {
	logger := logrus.New()
	ctx := common.WithLogger(context.Background(), logrus.NewEntry(logger))
	base := t.TempDir()
	miscRoot := filepath.Join(base, "misc")
	path := filepath.Join(miscRoot, "hostexecutor")
	require.NoError(t, os.MkdirAll(path, 0o700))
	workdir := filepath.Join(base, "workspace", "123", "owner", "repo")
	require.NoError(t, os.MkdirAll(workdir, 0o700))

	e := &HostEnvironment{
		Path:         path,
		Workdir:      workdir,
		CleanWorkdir: true,
		CleanUp: func() {
			_ = os.RemoveAll(miscRoot)
		},
		StdOut: os.Stdout,
	}
	require.NoError(t, e.Remove()(ctx))
	_, err := os.Stat(workdir)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestBuildWindowsWorkspaceKillScript(t *testing.T) {
	t.Run("single dir", func(t *testing.T) {
		s := buildWindowsWorkspaceKillScript([]string{`C:\workspace\job1`})
		assert.Contains(t, s, `$paths = @('C:\workspace\job1')`)
		// Self-PID guard is essential — without it the script could taskkill
		// the PowerShell process running it.
		assert.Contains(t, s, "$selfPid = $PID")
		assert.Contains(t, s, "$_.ProcessId -eq $selfPid")
		// Must match both ExecutablePath (binaries from the workspace) and
		// CommandLine (system binaries invoked with workspace paths in args),
		// both bounded by dir+separator so a name-prefix sibling is spared.
		assert.Contains(t, s, `$prefix = $p + '\'`)
		assert.Contains(t, s, "$_.ExecutablePath.StartsWith($prefix")
		assert.Contains(t, s, "$_.CommandLine.IndexOf($prefix")
		// Each matched PID must be tree-killed, not just stopped.
		assert.Contains(t, s, "taskkill.exe /PID $_.ProcessId /T /F")
	})

	t.Run("multiple dirs comma-separated", func(t *testing.T) {
		s := buildWindowsWorkspaceKillScript([]string{
			`C:\work\path`,
			`C:\work\workdir`,
			`C:\Users\runner\AppData\Local\Temp\job-42`,
		})
		assert.Contains(t, s, `'C:\work\path'`)
		assert.Contains(t, s, `'C:\work\workdir'`)
		assert.Contains(t, s, `'C:\Users\runner\AppData\Local\Temp\job-42'`)
		// Commas between entries — no trailing comma, no leading comma.
		assert.Contains(t, s, `'C:\work\path','C:\work\workdir',`)
	})

	t.Run("path with single quote is escaped", func(t *testing.T) {
		// In PowerShell single-quoted strings the only special char is the
		// quote itself, escaped by doubling. A workspace path that ever
		// contained `'` would inject a command into the script otherwise.
		s := buildWindowsWorkspaceKillScript([]string{`C:\work\it's\path`})
		assert.Contains(t, s, `'C:\work\it''s\path'`)
		// And it must NOT appear unescaped — otherwise the quote would
		// terminate the literal early.
		assert.NotContains(t, s, `'C:\work\it's\path'`)
	})

	t.Run("path with wildcard metacharacters is matched literally", func(t *testing.T) {
		// A path containing [ ] ? * must be embedded verbatim and matched with
		// ordinal String methods, not -like, otherwise the metacharacters would
		// be interpreted as wildcards and the leftover process could escape.
		s := buildWindowsWorkspaceKillScript([]string{`C:\work\[job]?1`})
		assert.Contains(t, s, `'C:\work\[job]?1'`)
		assert.NotContains(t, s, "-like")
		assert.Contains(t, s, "StartsWith")
		assert.Contains(t, s, "IndexOf")
	})

	t.Run("empty dir list still produces a valid script", func(t *testing.T) {
		s := buildWindowsWorkspaceKillScript(nil)
		// Empty array literal — script runs, matches nothing, is a no-op.
		assert.Contains(t, s, "$paths = @()")
		assert.Contains(t, s, "Get-CimInstance Win32_Process")
	})
}
