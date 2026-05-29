// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindGitSlug(t *testing.T) {
	assert := assert.New(t)

	slugTests := []struct {
		url      string // input
		provider string // expected result
		slug     string // expected result
	}{
		{"https://git-codecommit.us-east-1.amazonaws.com/v1/repos/my-repo-name", "CodeCommit", "my-repo-name"},
		{"ssh://git-codecommit.us-west-2.amazonaws.com/v1/repos/my-repo", "CodeCommit", "my-repo"},
		{"git@github.com:nektos/act.git", "GitHub", "nektos/act"},
		{"git@github.com:nektos/act", "GitHub", "nektos/act"},
		{"https://github.com/nektos/act.git", "GitHub", "nektos/act"},
		{"http://github.com/nektos/act.git", "GitHub", "nektos/act"},
		{"https://github.com/nektos/act", "GitHub", "nektos/act"},
		{"http://github.com/nektos/act", "GitHub", "nektos/act"},
		{"git+ssh://git@github.com/owner/repo.git", "GitHub", "owner/repo"},
		{"http://myotherrepo.com/act.git", "", "http://myotherrepo.com/act.git"},
	}

	for _, tt := range slugTests {
		provider, slug, err := findGitSlug(tt.url, "github.com")

		assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act
		assert.Equal(tt.provider, provider)
		assert.Equal(tt.slug, slug)
	}
}

func cleanGitHooks(dir string) error {
	hooksDir := filepath.Join(dir, ".git", "hooks")
	files, err := os.ReadDir(hooksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		relName := filepath.Join(hooksDir, f.Name())
		if err := os.Remove(relName); err != nil {
			return err
		}
	}
	return nil
}

func TestFindGitRemoteURL(t *testing.T) {
	assert := assert.New(t)

	basedir := t.TempDir()
	err := gitCmd("init", basedir)
	assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act
	err = cleanGitHooks(basedir)
	assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act

	remoteURL := "https://git-codecommit.us-east-1.amazonaws.com/v1/repos/my-repo-name"
	err = gitCmd("-C", basedir, "remote", "add", "origin", remoteURL)
	assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act

	u, err := findGitRemoteURL(context.Background(), basedir, "origin")
	assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(remoteURL, u)

	remoteURL = "git@github.com/AwesomeOwner/MyAwesomeRepo.git"
	err = gitCmd("-C", basedir, "remote", "add", "upstream", remoteURL)
	assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act
	u, err = findGitRemoteURL(context.Background(), basedir, "upstream")
	assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(remoteURL, u)
}

func TestGitFindRef(t *testing.T) {
	basedir := t.TempDir()

	for name, tt := range map[string]struct {
		Prepare func(t *testing.T, dir string)
		Assert  func(t *testing.T, ref string, err error)
	}{
		"new_repo": {
			Prepare: func(t *testing.T, dir string) {},
			Assert: func(t *testing.T, ref string, err error) {
				require.Error(t, err)
			},
		},
		"new_repo_with_commit": {
			Prepare: func(t *testing.T, dir string) {
				require.NoError(t, gitCmd("-C", dir, "commit", "--allow-empty", "-m", "msg"))
			},
			Assert: func(t *testing.T, ref string, err error) {
				require.NoError(t, err)
				require.Equal(t, "refs/heads/master", ref)
			},
		},
		"current_head_is_tag": {
			Prepare: func(t *testing.T, dir string) {
				require.NoError(t, gitCmd("-C", dir, "commit", "--allow-empty", "-m", "commit msg"))
				require.NoError(t, gitCmd("-C", dir, "tag", "v1.2.3"))
				require.NoError(t, gitCmd("-C", dir, "checkout", "v1.2.3"))
			},
			Assert: func(t *testing.T, ref string, err error) {
				require.NoError(t, err)
				require.Equal(t, "refs/tags/v1.2.3", ref)
			},
		},
		"current_head_is_same_as_tag": {
			Prepare: func(t *testing.T, dir string) {
				require.NoError(t, gitCmd("-C", dir, "commit", "--allow-empty", "-m", "1.4.2 release"))
				require.NoError(t, gitCmd("-C", dir, "tag", "v1.4.2"))
			},
			Assert: func(t *testing.T, ref string, err error) {
				require.NoError(t, err)
				require.Equal(t, "refs/tags/v1.4.2", ref)
			},
		},
		"current_head_is_not_tag": {
			Prepare: func(t *testing.T, dir string) {
				require.NoError(t, gitCmd("-C", dir, "commit", "--allow-empty", "-m", "msg"))
				require.NoError(t, gitCmd("-C", dir, "tag", "v1.4.2"))
				require.NoError(t, gitCmd("-C", dir, "commit", "--allow-empty", "-m", "msg2"))
			},
			Assert: func(t *testing.T, ref string, err error) {
				require.NoError(t, err)
				require.Equal(t, "refs/heads/master", ref)
			},
		},
		"current_head_is_another_branch": {
			Prepare: func(t *testing.T, dir string) {
				require.NoError(t, gitCmd("-C", dir, "checkout", "-b", "mybranch"))
				require.NoError(t, gitCmd("-C", dir, "commit", "--allow-empty", "-m", "msg"))
			},
			Assert: func(t *testing.T, ref string, err error) {
				require.NoError(t, err)
				require.Equal(t, "refs/heads/mybranch", ref)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(basedir, name)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, gitCmd("-C", dir, "init", "--initial-branch=master"))
			require.NoError(t, cleanGitHooks(dir))
			tt.Prepare(t, dir)
			ref, err := FindGitRef(context.Background(), dir)
			tt.Assert(t, ref, err)
		})
	}
}

func TestGitCloneExecutor(t *testing.T) {
	// Build a local bare "remote" so this runs offline and fast. The cases below mirror
	// the tag/branch/sha/short-sha ref paths the executor handles, formerly exercised by
	// cloning actions/checkout and anchore/scan-action over the network.
	remoteDir := t.TempDir()
	require.NoError(t, gitCmd("init", "--bare", "--initial-branch=main", remoteDir))

	workDir := t.TempDir()
	require.NoError(t, gitCmd("clone", remoteDir, workDir))
	require.NoError(t, gitCmd("-C", workDir, "checkout", "-b", "main"))
	require.NoError(t, gitCmd("-C", workDir, "commit", "--allow-empty", "-m", "initial"))
	require.NoError(t, gitCmd("-C", workDir, "tag", "v2"))
	require.NoError(t, gitCmd("-C", workDir, "push", "-u", "origin", "main"))
	require.NoError(t, gitCmd("-C", workDir, "push", "origin", "v2"))

	// A branch with a dash in the name (mirrors the historical scan-action@act-fails case).
	require.NoError(t, gitCmd("-C", workDir, "checkout", "-b", "act-fails"))
	require.NoError(t, gitCmd("-C", workDir, "commit", "--allow-empty", "-m", "branch-commit"))
	require.NoError(t, gitCmd("-C", workDir, "push", "origin", "act-fails"))

	out, err := exec.Command("git", "-C", workDir, "rev-parse", "main").Output()
	require.NoError(t, err)
	fullSha := strings.TrimSpace(string(out))

	for name, tt := range map[string]struct {
		Err error
		Ref string
	}{
		"tag": {
			Err: nil,
			Ref: "v2",
		},
		"branch": {
			Err: nil,
			Ref: "act-fails",
		},
		"sha": {
			Err: nil,
			Ref: fullSha,
		},
		"short-sha": {
			Err: &Error{ErrShortRef, fullSha},
			Ref: fullSha[:7],
		},
	} {
		t.Run(name, func(t *testing.T) {
			clone := NewGitCloneExecutor(NewGitCloneExecutorInput{
				URL: remoteDir,
				Ref: tt.Ref,
				Dir: t.TempDir(),
			})

			err := clone(context.Background())
			if tt.Err != nil {
				assert.Error(t, err) //nolint:testifylint // pre-existing issue from nektos/act
				assert.Equal(t, tt.Err, err)
			} else {
				assert.Empty(t, err) //nolint:testifylint // pre-existing issue from nektos/act
			}
		})
	}
}

func TestGitCloneExecutorNonFastForwardRef(t *testing.T) {
	// Simulate the scenario where a remote ref (e.g. a GitHub PR head ref) changes
	// non-fast-forward between two fetches. Before the fix, the fetch used Force=false,
	// causing go-git to return ErrForceNeeded and short-circuit the checkout.

	// Create a bare "remote" repo with an initial commit on main and a feature branch.
	remoteDir := t.TempDir()
	require.NoError(t, gitCmd("init", "--bare", "--initial-branch=main", remoteDir))

	// We need a working clone to push commits from.
	workDir := t.TempDir()
	require.NoError(t, gitCmd("clone", remoteDir, workDir))
	require.NoError(t, gitCmd("-C", workDir, "checkout", "-b", "main"))
	require.NoError(t, gitCmd("-C", workDir, "commit", "--allow-empty", "-m", "initial"))
	require.NoError(t, gitCmd("-C", workDir, "push", "-u", "origin", "main"))

	// Create a feature branch (simulates refs/pull/N/head).
	require.NoError(t, gitCmd("-C", workDir, "checkout", "-b", "feature"))
	require.NoError(t, gitCmd("-C", workDir, "commit", "--allow-empty", "-m", "feature-1"))
	require.NoError(t, gitCmd("-C", workDir, "push", "origin", "feature"))

	// First clone via the executor — should succeed and cache the repo.
	cloneDir := t.TempDir()
	clone := NewGitCloneExecutor(NewGitCloneExecutorInput{
		URL: remoteDir,
		Ref: "main",
		Dir: cloneDir,
	})
	require.NoError(t, clone(context.Background()))

	// Now force-push the feature branch to a non-fast-forward commit (simulates
	// a PR rebase). This makes refs/heads/feature non-fast-forward.
	require.NoError(t, gitCmd("-C", workDir, "checkout", "main"))
	require.NoError(t, gitCmd("-C", workDir, "branch", "-D", "feature"))
	require.NoError(t, gitCmd("-C", workDir, "checkout", "-b", "feature"))
	require.NoError(t, gitCmd("-C", workDir, "commit", "--allow-empty", "-m", "feature-rewritten"))
	require.NoError(t, gitCmd("-C", workDir, "push", "--force", "origin", "feature"))

	// Also advance main so we can verify the clone picks up the new commit.
	require.NoError(t, gitCmd("-C", workDir, "checkout", "main"))
	require.NoError(t, gitCmd("-C", workDir, "commit", "--allow-empty", "-m", "second"))
	require.NoError(t, gitCmd("-C", workDir, "push", "origin", "main"))

	// Second clone to the same directory — before the fix this returned ErrForceNeeded
	// and left the working tree at the old commit.
	err := clone(context.Background())
	require.NoError(t, err, "fetch with non-fast-forward refs must not fail when Force=true")

	// Verify the working tree was actually updated to the latest main commit.
	out, err := exec.Command("git", "-C", cloneDir, "log", "--oneline", "-1", "--format=%s").Output()
	require.NoError(t, err)
	assert.Equal(t, "second", strings.TrimSpace(string(out)), "working tree should be at the latest commit")
}

func TestGitCloneExecutorOfflineMode(t *testing.T) {
	// Build a local "remote" with a single commit on main.
	remoteDir := t.TempDir()
	require.NoError(t, gitCmd("init", "--bare", "--initial-branch=main", remoteDir))
	workDir := t.TempDir()
	require.NoError(t, gitCmd("clone", remoteDir, workDir))
	require.NoError(t, gitCmd("-C", workDir, "checkout", "-b", "main"))
	require.NoError(t, gitCmd("-C", workDir, "commit", "--allow-empty", "-m", "initial"))
	require.NoError(t, gitCmd("-C", workDir, "push", "-u", "origin", "main"))

	// Prime the cache with an online clone of main.
	cacheDir := t.TempDir()
	require.NoError(t, NewGitCloneExecutor(NewGitCloneExecutorInput{
		URL: remoteDir,
		Ref: "main",
		Dir: cacheDir,
	})(context.Background()))

	t.Run("cached branch resolves without fetching", func(t *testing.T) {
		// Offline reuse of a cached branch must succeed even though ResolveRevision(input.Ref)
		// finds no local refs/heads/<ref>.
		err := NewGitCloneExecutor(NewGitCloneExecutorInput{
			URL:         remoteDir,
			Ref:         "main",
			Dir:         cacheDir,
			OfflineMode: true,
		})(context.Background())
		require.NoError(t, err)

		out, err := exec.Command("git", "-C", cacheDir, "log", "--oneline", "-1", "--format=%s").Output()
		require.NoError(t, err)
		assert.Equal(t, "initial", strings.TrimSpace(string(out)))
	})

	t.Run("unresolvable cached ref returns error", func(t *testing.T) {
		// The ref was never cached; offline mode cannot resolve it and must return an error.
		err := NewGitCloneExecutor(NewGitCloneExecutorInput{
			URL:         remoteDir,
			Ref:         "never-fetched",
			Dir:         cacheDir,
			OfflineMode: true,
		})(context.Background())
		require.Error(t, err)
	})
}

func gitCmd(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Inject a deterministic identity and ignore the host's global/system config so commits
	// succeed regardless of the host having no user.name/user.email (e.g. CI, GITHUB_ACTIONS
	// unset) or a global commit.gpgsign, and without mutating the developer's ~/.gitconfig.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Unit Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Unit Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)

	err := cmd.Run()
	if exitError, ok := err.(*exec.ExitError); ok {
		if waitStatus, ok := exitError.Sys().(syscall.WaitStatus); ok {
			return fmt.Errorf("Exit error %d", waitStatus.ExitStatus())
		}
		return exitError
	}
	return nil
}

func TestAcquireCloneLock(t *testing.T) {
	t.Run("same directory serializes", func(t *testing.T) {
		dir := t.TempDir()

		unlock1 := AcquireCloneLock(dir)

		secondAcquired := make(chan struct{})
		go func() {
			unlock := AcquireCloneLock(dir)
			close(secondAcquired)
			unlock()
		}()

		select {
		case <-secondAcquired:
			t.Fatal("second acquire should block while first holds the lock")
		case <-time.After(50 * time.Millisecond):
		}

		unlock1()

		select {
		case <-secondAcquired:
		case <-time.After(time.Second):
			t.Fatal("second acquire should proceed after first releases the lock")
		}
	})

	t.Run("different directories do not block", func(t *testing.T) {
		dirA := t.TempDir()
		dirB := t.TempDir()

		unlockA := AcquireCloneLock(dirA)
		defer unlockA()

		done := make(chan struct{})
		go func() {
			unlock := AcquireCloneLock(dirB)
			unlock()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("acquire on a different directory must not block")
		}
	})

	t.Run("same directory reuses the same mutex", func(t *testing.T) {
		dir := t.TempDir()

		v1, _ := cloneLocks.LoadOrStore(dir, &sync.Mutex{})
		v2, _ := cloneLocks.LoadOrStore(dir, &sync.Mutex{})
		require.Same(t, v1, v2)
	})
}
