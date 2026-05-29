// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerCloneEnvsReturnsTaskLocalCopy(t *testing.T) {
	r := &Runner{
		envs: map[string]string{
			"ACTIONS_CACHE_URL":   "http://cache.example",
			"ACTIONS_RUNTIME_URL": "http://runner.example",
		},
	}

	cloned := r.cloneEnvs()
	require.Equal(t, r.envs, cloned)

	cloned["ACTIONS_RUNTIME_TOKEN"] = "task-token"
	cloned["ACTIONS_ID_TOKEN_REQUEST_URL"] = "http://oidc.example"

	assert.NotContains(t, r.envs, "ACTIONS_RUNTIME_TOKEN")
	assert.NotContains(t, r.envs, "ACTIONS_ID_TOKEN_REQUEST_URL")
	assert.Equal(t, "http://cache.example", r.envs["ACTIONS_CACHE_URL"])
}

// Regression test for #958: concurrent tasks writing task-specific env keys
// used to race on the shared r.envs map and crash the runner with
// "fatal error: concurrent map writes". Each task must mutate its own clone.
func TestRunnerCloneEnvsConcurrentMutation(t *testing.T) {
	r := &Runner{
		envs: map[string]string{
			"ACTIONS_CACHE_URL":   "http://cache.example",
			"ACTIONS_RUNTIME_URL": "http://runner.example",
		},
	}

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			envs := r.cloneEnvs()
			envs["ACTIONS_RUNTIME_TOKEN"] = "task-token"
			envs["ACTIONS_ID_TOKEN_REQUEST_URL"] = "http://oidc.example"
			envs["ACTIONS_ID_TOKEN_REQUEST_TOKEN"] = "oidc-token"
		}()
	}
	wg.Wait()

	assert.NotContains(t, r.envs, "ACTIONS_RUNTIME_TOKEN")
	assert.NotContains(t, r.envs, "ACTIONS_ID_TOKEN_REQUEST_URL")
	assert.NotContains(t, r.envs, "ACTIONS_ID_TOKEN_REQUEST_TOKEN")
}
