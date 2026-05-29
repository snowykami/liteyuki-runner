// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"testing"

	"gitea.com/gitea/runner/internal/pkg/config"

	"github.com/stretchr/testify/assert"
)

func TestMatchAllowedRepo(t *testing.T) {
	tests := []struct {
		name         string
		targetRepo   string
		allowedRepos []string
		want         bool
	}{
		{
			name:       "empty list allows all",
			targetRepo: "owner/repo",
			want:       true,
		},
		{
			name:         "exact match",
			targetRepo:   "owner/repo",
			allowedRepos: []string{"owner/repo"},
			want:         true,
		},
		{
			name:         "owner wildcard",
			targetRepo:   "owner/repo",
			allowedRepos: []string{"owner/*"},
			want:         true,
		},
		{
			name:         "repository wildcard",
			targetRepo:   "owner/repo",
			allowedRepos: []string{"*/repo"},
			want:         true,
		},
		{
			name:         "case insensitive match",
			targetRepo:   "Owner/Repo",
			allowedRepos: []string{"owner/repo"},
			want:         true,
		},
		{
			name:         "invalid target is rejected",
			targetRepo:   "owner/repo/extra",
			allowedRepos: []string{"owner/*"},
			want:         false,
		},
		{
			name:         "invalid allowed repo is ignored",
			targetRepo:   "owner/repo",
			allowedRepos: []string{"invalid", "other/*"},
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, matchAllowedRepo(tt.targetRepo, tt.allowedRepos))
		})
	}
}

func TestRunnerCanRunRepoBlacklistMode(t *testing.T) {
	r := &Runner{
		cfg: &config.Config{
			Runner: config.Runner{
				AllowedRepos:  []string{"blocked/*"},
				BlacklistMode: true,
			},
		},
	}

	assert.False(t, r.canRunRepo("blocked/repo"))
	assert.True(t, r.canRunRepo("allowed/repo"))
}
