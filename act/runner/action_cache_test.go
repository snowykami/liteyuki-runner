// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2023 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	// Fixed identity and host-config isolation so commits succeed offline regardless of the
	// host's git config (mirrors gitCmd in act/common/git).
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// TestShortShaActionRejected verifies a `uses` ref that is a shortened commit SHA is rejected
// with a clear error. The action is resolved from a local repo (via DefaultActionInstance) so
// this runs offline.
func TestShortShaActionRejected(t *testing.T) {
	// a local "remote" action repo at <root>/actions/hello-world-docker-action
	actionRoot := t.TempDir()
	repo := filepath.Join(actionRoot, "actions", "hello-world-docker-action")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	runGit(t, "", "init", "--initial-branch=main", repo)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "action.yml"),
		[]byte("name: hello\nruns:\n  using: node24\n  main: index.js\n"), 0o644))
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "initial")
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	shortSha := strings.TrimSpace(string(out))[:7]

	// a workflow that uses the action at the short SHA
	wfDir := filepath.Join(t.TempDir(), "wf")
	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	wf := fmt.Sprintf("on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/hello-world-docker-action@%s\n", shortSha)
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "push.yml"), []byte(wf), 0o644))

	runner, err := New(&Config{
		Workdir:               wfDir,
		EventName:             "push",
		Platforms:             map[string]string{"ubuntu-latest": baseImage},
		GitHubInstance:        "github.com",
		DefaultActionInstance: actionRoot,
		ContainerMaxLifetime:  time.Hour,
	})
	require.NoError(t, err)
	planner, err := model.NewWorkflowPlanner(wfDir, true)
	require.NoError(t, err)
	plan, err := planner.PlanEvent("push")
	require.NoError(t, err)

	err = runner.NewPlanExecutor(plan)(common.WithDryrun(context.Background(), true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shortened version of a commit SHA")
}

func TestActionCache(t *testing.T) {
	a := assert.New(t)
	ctx := context.Background()

	// Build a local bare repo with a `js` action dir so this runs offline (formerly cloned
	// github.com/nektos/act-test-actions over the network). allowAnySHA1InWant lets the
	// "Fetch Sha" case fetch a commit hash directly.
	remoteDir := t.TempDir()
	runGit(t, "", "init", "--bare", "--initial-branch=main", remoteDir)
	runGit(t, remoteDir, "config", "uploadpack.allowAnySHA1InWant", "true")

	workDir := t.TempDir()
	runGit(t, "", "clone", remoteDir, workDir)
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "js"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "js", "action.yml"),
		[]byte("name: js\nruns:\n  using: node24\n  main: index.js\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "js", "index.js"),
		[]byte("console.log('hello');\n"), 0o644))
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "commit", "-m", "initial")
	runGit(t, workDir, "push", "-u", "origin", "main")

	out, err := exec.Command("git", "-C", workDir, "rev-parse", "main").Output()
	require.NoError(t, err)
	fullSha := strings.TrimSpace(string(out))

	cache := &GoGitActionCache{
		Path: t.TempDir(),
	}
	cacheDir := "local/act-test-actions"
	refs := []struct {
		Name string
		Ref  string
	}{
		{Name: "Fetch Branch Name", Ref: "main"},
		{Name: "Fetch Branch Name Absolutely", Ref: "refs/heads/main"},
		{Name: "Fetch HEAD", Ref: "HEAD"},
		{Name: "Fetch Sha", Ref: fullSha},
	}
	for _, c := range refs {
		t.Run(c.Name, func(t *testing.T) {
			sha, err := cache.Fetch(ctx, cacheDir, remoteDir, c.Ref, "")
			if !a.NoError(err) || !a.NotEmpty(sha) { //nolint:testifylint // pre-existing issue from nektos/act
				return
			}
			atar, err := cache.GetTarArchive(ctx, cacheDir, sha, "js")
			// NotNil, not NotEmpty: atar is a live io.PipeReader whose producer goroutine is
			// writing concurrently; NotEmpty deep-reflects over its internals and races.
			if !a.NoError(err) || !a.NotNil(atar) { //nolint:testifylint // pre-existing issue from nektos/act
				return
			}
			// GetTarArchive streams from a background goroutine walking the shared repo.
			// Drain and close so it finishes before the next subtest fetches into the same
			// repo; otherwise the lingering walk races with that fetch.
			defer func() {
				_, _ = io.Copy(io.Discard, atar)
				_ = atar.Close()
			}()
			mytar := tar.NewReader(atar)
			th, err := mytar.Next()
			if !a.NoError(err) || !a.NotEqual(0, th.Size) { //nolint:testifylint // pre-existing issue from nektos/act
				return
			}
			buf := &bytes.Buffer{}
			// G110: Potential DoS vulnerability via decompression bomb (gosec)
			_, err = io.Copy(buf, mytar)
			a.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act
			str := buf.String()
			a.NotEmpty(str)
		})
	}
}
