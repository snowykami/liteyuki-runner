// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gitea.com/gitea/runner/internal/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerCleanupStaleTaskDirs(t *testing.T) {
	now := time.Date(2026, time.April, 29, 20, 0, 0, 0, time.UTC)
	workdirRoot := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.MkdirAll(workdirRoot, 0o700))

	oldTask := filepath.Join(workdirRoot, "1001")
	freshTask := filepath.Join(workdirRoot, "1002")
	nonTask := filepath.Join(workdirRoot, "shared")
	alphaNumericTask := filepath.Join(workdirRoot, "123abc")
	for _, path := range []string{oldTask, freshTask, nonTask, alphaNumericTask} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}

	require.NoError(t, os.Chtimes(oldTask, now.Add(-3*time.Hour), now.Add(-3*time.Hour)))
	require.NoError(t, os.Chtimes(freshTask, now.Add(-30*time.Minute), now.Add(-30*time.Minute)))
	require.NoError(t, os.Chtimes(nonTask, now.Add(-5*time.Hour), now.Add(-5*time.Hour)))
	require.NoError(t, os.Chtimes(alphaNumericTask, now.Add(-5*time.Hour), now.Add(-5*time.Hour)))

	r := &Runner{
		cfg: &config.Config{
			Runner: config.Runner{
				WorkdirCleanupAge: 2 * time.Hour,
			},
		},
		now: func() time.Time { return now },
	}

	r.cleanupStaleTaskDirs(context.Background(), workdirRoot)

	assert.NoDirExists(t, oldTask)
	assert.DirExists(t, freshTask)
	assert.DirExists(t, nonTask)
	assert.DirExists(t, alphaNumericTask)
}

func TestRunnerCleanupStaleTaskDirsMissingRoot(t *testing.T) {
	r := &Runner{
		cfg: &config.Config{
			Runner: config.Runner{WorkdirCleanupAge: time.Hour},
		},
		now: time.Now,
	}

	// Must be a silent no-op rather than a warning or panic when the root
	// has not yet been created (e.g. the runner has never executed a task).
	r.cleanupStaleTaskDirs(context.Background(), filepath.Join(t.TempDir(), "missing"))
}

func TestRunnerCleanupStaleTaskDirsHonorsContext(t *testing.T) {
	now := time.Date(2026, time.April, 29, 20, 0, 0, 0, time.UTC)
	workdirRoot := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.MkdirAll(workdirRoot, 0o700))

	for i := 1001; i <= 1003; i++ {
		dir := filepath.Join(workdirRoot, strconv.Itoa(i))
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.Chtimes(dir, now.Add(-3*time.Hour), now.Add(-3*time.Hour)))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &Runner{
		cfg: &config.Config{
			Runner: config.Runner{WorkdirCleanupAge: time.Hour},
		},
		now: func() time.Time { return now },
	}

	r.cleanupStaleTaskDirs(ctx, workdirRoot)

	for i := 1001; i <= 1003; i++ {
		assert.DirExists(t, filepath.Join(workdirRoot, strconv.Itoa(i)))
	}
}

func TestRunnerShouldRunIdleCleanupThrottles(t *testing.T) {
	now := time.Date(2026, time.April, 29, 20, 0, 0, 0, time.UTC)
	r := &Runner{
		cfg: &config.Config{
			Container: config.Container{
				BindWorkdir: true,
			},
			Runner: config.Runner{
				WorkdirCleanupAge:   24 * time.Hour,
				IdleCleanupInterval: time.Hour,
			},
		},
		now: func() time.Time { return now },
	}

	assert.True(t, r.shouldRunIdleCleanup())

	now = now.Add(30 * time.Minute)
	assert.False(t, r.shouldRunIdleCleanup())

	now = now.Add(31 * time.Minute)
	assert.True(t, r.shouldRunIdleCleanup())
}

func TestRunnerShouldRunIdleCleanupSkipsWhenJobRunning(t *testing.T) {
	r := &Runner{
		cfg: &config.Config{
			Container: config.Container{
				BindWorkdir: true,
			},
			Runner: config.Runner{
				WorkdirCleanupAge:   24 * time.Hour,
				IdleCleanupInterval: time.Minute,
			},
		},
		now: time.Now,
	}
	r.runningCount.Store(1)

	assert.False(t, r.shouldRunIdleCleanup())
}

func TestRunnerShouldRunIdleCleanupSkipsWhenBindWorkdirDisabled(t *testing.T) {
	r := &Runner{
		cfg: &config.Config{
			Runner: config.Runner{
				WorkdirCleanupAge:   24 * time.Hour,
				IdleCleanupInterval: time.Minute,
			},
		},
		now: time.Now,
	}

	assert.False(t, r.shouldRunIdleCleanup())
}

func TestRunnerShouldRunIdleCleanupSkipsWhenDisabled(t *testing.T) {
	now := time.Date(2026, time.April, 29, 20, 0, 0, 0, time.UTC)

	t.Run("cleanup age disabled", func(t *testing.T) {
		r := &Runner{
			cfg: &config.Config{
				Container: config.Container{
					BindWorkdir: true,
				},
				Runner: config.Runner{
					WorkdirCleanupAge:   -1,
					IdleCleanupInterval: time.Minute,
				},
			},
			now: func() time.Time { return now },
		}

		assert.False(t, r.shouldRunIdleCleanup())
	})

	t.Run("idle interval disabled", func(t *testing.T) {
		r := &Runner{
			cfg: &config.Config{
				Container: config.Container{
					BindWorkdir: true,
				},
				Runner: config.Runner{
					WorkdirCleanupAge:   24 * time.Hour,
					IdleCleanupInterval: -1,
				},
			},
			now: func() time.Time { return now },
		}

		assert.False(t, r.shouldRunIdleCleanup())
	})
}

// TestRunnerOnIdleIntegratesCleanup wires the full OnIdle entry point and
// confirms it walks workdir_parent (after the leading-slash trim that
// matches the production path construction) and removes stale numeric dirs.
func TestRunnerOnIdleIntegratesCleanup(t *testing.T) {
	now := time.Date(2026, time.April, 29, 20, 0, 0, 0, time.UTC)
	root := t.TempDir()
	stale := filepath.Join(root, "1234")
	require.NoError(t, os.MkdirAll(stale, 0o700))
	require.NoError(t, os.Chtimes(stale, now.Add(-48*time.Hour), now.Add(-48*time.Hour)))

	r := &Runner{
		cfg: &config.Config{
			Container: config.Container{
				BindWorkdir:   true,
				WorkdirParent: root, // leading slash absent, OnIdle reattaches it
			},
			Runner: config.Runner{
				WorkdirCleanupAge:   24 * time.Hour,
				IdleCleanupInterval: time.Minute,
			},
		},
		now: func() time.Time { return now },
	}

	r.OnIdle(context.Background())

	assert.NoDirExists(t, stale)
}

// TestRunnerOnIdleSkipsWhenAlreadyCancelled verifies a pre-cancelled ctx
// short-circuits cleanup before any directory entry is touched.
func TestRunnerOnIdleSkipsWhenAlreadyCancelled(t *testing.T) {
	now := time.Date(2026, time.April, 29, 20, 0, 0, 0, time.UTC)
	root := t.TempDir()
	stale := filepath.Join(root, "1234")
	require.NoError(t, os.MkdirAll(stale, 0o700))
	require.NoError(t, os.Chtimes(stale, now.Add(-48*time.Hour), now.Add(-48*time.Hour)))

	r := &Runner{
		cfg: &config.Config{
			Container: config.Container{
				BindWorkdir:   true,
				WorkdirParent: root,
			},
			Runner: config.Runner{
				WorkdirCleanupAge:   24 * time.Hour,
				IdleCleanupInterval: time.Minute,
			},
		},
		now: func() time.Time { return now },
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.OnIdle(ctx)

	assert.DirExists(t, stale)
}
