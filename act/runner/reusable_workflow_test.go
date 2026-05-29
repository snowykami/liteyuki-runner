// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/common/git"
	"gitea.com/gitea/runner/act/model"

	"github.com/stretchr/testify/require"
)

// Regression test for go-gitea/gitea#37483: a remote reusable workflow at a moving
// ref (branch/tag) must reflect the new tip on every invocation, not stay pinned
// to the cache populated on the first run.
func TestReusableWorkflowCachedBranchRefRefreshes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in PATH")
	}

	remoteDir := t.TempDir()
	gitMust(t, "", "init", "--bare", "--initial-branch=master", remoteDir)

	workDir := t.TempDir()
	gitMust(t, "", "clone", remoteDir, workDir)
	gitMust(t, workDir, "config", "user.email", "test@test")
	gitMust(t, workDir, "config", "user.name", "test")
	gitMust(t, workDir, "checkout", "-b", "master")

	const workflowPath = ".gitea/workflows/reusable.yml"
	tmpl := func(tag string) string {
		return "name: reusable\non:\n  workflow_call:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo " + tag + "\n"
	}

	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".gitea/workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, workflowPath), []byte(tmpl("v1")), 0o644))
	gitMust(t, workDir, "add", workflowPath)
	gitMust(t, workDir, "commit", "-m", "v1")
	gitMust(t, workDir, "push", "-u", "origin", "master")

	rc := &RunContext{
		Config: &Config{},
		Run: &model.Run{
			JobID: "j1",
			Workflow: &model.Workflow{
				Name: "wf",
				Jobs: map[string]*model.Job{"j1": {}},
			},
		},
	}
	cacheDir := t.TempDir()

	require.NoError(t, cloneRemoteReusableWorkflow(rc, remoteDir, "master", cacheDir, "")(context.Background()))
	got, err := os.ReadFile(filepath.Join(cacheDir, workflowPath))
	require.NoError(t, err)
	require.Equal(t, tmpl("v1"), string(got))

	// Branch tip moves; cache key (cacheDir) does not.
	require.NoError(t, os.WriteFile(filepath.Join(workDir, workflowPath), []byte(tmpl("v2")), 0o644))
	gitMust(t, workDir, "commit", "-am", "v2")
	gitMust(t, workDir, "push", "origin", "master")

	require.NoError(t, cloneRemoteReusableWorkflow(rc, remoteDir, "master", cacheDir, "")(context.Background()))
	got, err = os.ReadFile(filepath.Join(cacheDir, workflowPath))
	require.NoError(t, err)
	require.Equal(t, tmpl("v2"), string(got), "cached workflow file must reflect the updated branch tip")
}

func TestNewReusableWorkflowExecutorHoldsCloneLock(t *testing.T) {
	workflowDir := t.TempDir()

	unlockOnce := sync.OnceFunc(git.AcquireCloneLock(workflowDir))
	defer unlockOnce()

	plannerCalled := make(chan struct{})

	origPlanner := modelNewWorkflowPlanner
	modelNewWorkflowPlanner = func(string, bool) (model.WorkflowPlanner, error) {
		close(plannerCalled)
		return nil, errors.New("stop")
	}
	defer func() { modelNewWorkflowPlanner = origPlanner }()

	rc := &RunContext{
		Config: &Config{},
		Run:    &model.Run{Workflow: &model.Workflow{Jobs: map[string]*model.Job{}}},
	}
	exec := newReusableWorkflowExecutor(rc, workflowDir, "reusable.yml")

	done := make(chan error, 1)
	go func() { done <- exec(context.Background()) }()

	select {
	case <-plannerCalled:
		t.Fatal("planner ran while clone lock was held")
	case err := <-done:
		t.Fatalf("executor returned before planner was reached: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	unlockOnce()

	select {
	case <-plannerCalled:
	case <-time.After(time.Second):
		t.Fatal("planner not called after lock was released")
	}

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("executor did not return after planner ran")
	}
}

func TestGetGitCloneTokenWithSchemalessGiteaInstance(t *testing.T) {
	conf := &Config{
		GitHubInstance: "gitea.example.net",
		Secrets: map[string]string{
			"GITEA_TOKEN": "token-value",
		},
	}

	token := getGitCloneToken(conf, "https://gitea.example.net/actions/tools")

	require.Equal(t, "token-value", token)
}

func TestShouldCloneURLUseToken(t *testing.T) {
	tests := []struct {
		name        string
		instanceURL string
		cloneURL    string
		want        bool
	}{
		{
			name:        "same host with schemaless instance",
			instanceURL: "gitea.example.net",
			cloneURL:    "https://gitea.example.net/actions/tools",
			want:        true,
		},
		{
			name:        "same host with schemaless instance and port",
			instanceURL: "gitea.example.net:3000",
			cloneURL:    "https://gitea.example.net:3000/actions/tools",
			want:        true,
		},
		{
			name:        "different host",
			instanceURL: "gitea.example.net",
			cloneURL:    "https://github.com/actions/tools",
			want:        false,
		},
		{
			name:        "embedded basic auth",
			instanceURL: "gitea.example.net",
			cloneURL:    "https://user:pass@gitea.example.net/actions/tools",
			want:        false,
		},
		{
			name:        "invalid clone URL",
			instanceURL: "gitea.example.net",
			cloneURL:    "://gitea.example.net/actions/tools",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldCloneURLUseToken(tt.instanceURL, tt.cloneURL))
		})
	}
}

func gitMust(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, string(out))
}
