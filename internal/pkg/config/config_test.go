// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefault_RejectsExternalServerWithoutSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cache:
  enabled: true
  external_server: "http://cache.invalid/"
`), 0o600))

	_, err := LoadDefault(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "external_secret")
}

func TestLoadDefault_AcceptsExternalServerWithSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cache:
  enabled: true
  external_server: "http://cache.invalid/"
  external_secret: "shh"
`), 0o600))

	_, err := LoadDefault(path)
	require.NoError(t, err)
}

func TestLoadDefault_LoadsAllowedRepos(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  allowed_repos:
    - "owner/repo"
    - "org/*"
  blacklist_mode: true
  reject_text: "No access for {REPO} on {RUNNER}."
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner/repo", "org/*"}, cfg.Runner.AllowedRepos)
	assert.True(t, cfg.Runner.BlacklistMode)
	assert.Equal(t, "No access for {REPO} on {RUNNER}.", cfg.Runner.RejectText)
}

func TestLoadDefault_DefaultsWorkdirCleanupAge(t *testing.T) {
	cfg, err := LoadDefault("")
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, cfg.Runner.WorkdirCleanupAge)
	assert.Equal(t, 10*time.Minute, cfg.Runner.IdleCleanupInterval)
}

func TestLoadDefault_UsesConfiguredWorkdirCleanupAge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  workdir_cleanup_age: 2h30m
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, 150*time.Minute, cfg.Runner.WorkdirCleanupAge)
}

func TestLoadDefault_UsesConfiguredIdleCleanupInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  idle_cleanup_interval: 45m
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, 45*time.Minute, cfg.Runner.IdleCleanupInterval)
}

func TestLoadDefault_AllowsDisablingWorkdirCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  workdir_cleanup_age: 0s
  idle_cleanup_interval: 0s
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), cfg.Runner.WorkdirCleanupAge)
	assert.Equal(t, time.Duration(0), cfg.Runner.IdleCleanupInterval)
}

func TestLoadDefault_AllowsNegativeWorkdirCleanupValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  workdir_cleanup_age: -1s
  idle_cleanup_interval: -1s
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, -1*time.Second, cfg.Runner.WorkdirCleanupAge)
	assert.Equal(t, -1*time.Second, cfg.Runner.IdleCleanupInterval)
}

// TestLoadDefault_MalformedYAMLReturnsParseError pins the error surfaced for
// invalid YAML to the canonical "parse config file" message rather than the
// "for defaults metadata" variant — i.e. the main yaml.Unmarshal runs first.
func TestLoadDefault_MalformedYAMLReturnsParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("runner:\n  capacity: [unterminated\n"), 0o600))

	_, err := LoadDefault(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse config file")
	assert.NotContains(t, err.Error(), "defaults metadata")
}
