// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package model

import (
	"context"
	"errors"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestSetRef(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	oldFindGitRef := findGitRef
	oldFindGitRevision := findGitRevision
	defer func() { findGitRef = oldFindGitRef }()
	defer func() { findGitRevision = oldFindGitRevision }()

	findGitRef = func(ctx context.Context, file string) (string, error) {
		return "refs/heads/master", nil
	}

	findGitRevision = func(ctx context.Context, file string) (string, string, error) {
		return "", "1234fakesha", nil
	}

	tables := []struct {
		eventName string
		event     map[string]any
		ref       string
		refName   string
	}{
		{
			eventName: "pull_request_target",
			event:     map[string]any{},
			ref:       "refs/heads/master",
			refName:   "master",
		},
		{
			eventName: "pull_request",
			event: map[string]any{
				"number": 1234.,
			},
			ref:     "refs/pull/1234/merge",
			refName: "1234/merge",
		},
		{
			eventName: "deployment",
			event: map[string]any{
				"deployment": map[string]any{
					"ref": "refs/heads/somebranch",
				},
			},
			ref:     "refs/heads/somebranch",
			refName: "somebranch",
		},
		{
			eventName: "release",
			event: map[string]any{
				"release": map[string]any{
					"tag_name": "v1.0.0",
				},
			},
			ref:     "refs/tags/v1.0.0",
			refName: "v1.0.0",
		},
		{
			eventName: "push",
			event: map[string]any{
				"ref": "refs/heads/somebranch",
			},
			ref:     "refs/heads/somebranch",
			refName: "somebranch",
		},
		{
			eventName: "unknown",
			event: map[string]any{
				"repository": map[string]any{
					"default_branch": "main",
				},
			},
			ref:     "refs/heads/main",
			refName: "main",
		},
		{
			eventName: "no-event",
			event:     map[string]any{},
			ref:       "refs/heads/master",
			refName:   "master",
		},
	}

	for _, table := range tables {
		t.Run(table.eventName, func(t *testing.T) {
			ghc := &GithubContext{
				EventName: table.eventName,
				BaseRef:   "master",
				Event:     table.event,
			}

			ghc.SetRef(context.Background(), "main", "/some/dir")
			ghc.SetRefTypeAndName()

			assert.Equal(t, table.ref, ghc.Ref)
			assert.Equal(t, table.refName, ghc.RefName)
		})
	}

	t.Run("no-default-branch", func(t *testing.T) {
		findGitRef = func(ctx context.Context, file string) (string, error) {
			return "", errors.New("no default branch")
		}

		ghc := &GithubContext{
			EventName: "no-default-branch",
			Event:     map[string]any{},
		}

		ghc.SetRef(context.Background(), "", "/some/dir")

		assert.Equal(t, "refs/heads/master", ghc.Ref)
	})
}

func TestSetSha(t *testing.T) {
	log.SetLevel(log.DebugLevel)

	oldFindGitRef := findGitRef
	oldFindGitRevision := findGitRevision
	defer func() { findGitRef = oldFindGitRef }()
	defer func() { findGitRevision = oldFindGitRevision }()

	findGitRef = func(ctx context.Context, file string) (string, error) {
		return "refs/heads/master", nil
	}

	findGitRevision = func(ctx context.Context, file string) (string, string, error) {
		return "", "1234fakesha", nil
	}

	tables := []struct {
		eventName string
		event     map[string]any
		sha       string
	}{
		{
			eventName: "pull_request_target",
			event: map[string]any{
				"pull_request": map[string]any{
					"base": map[string]any{
						"sha": "pr-base-sha",
					},
				},
			},
			sha: "pr-base-sha",
		},
		{
			eventName: "pull_request",
			event: map[string]any{
				"number": 1234.,
			},
			sha: "1234fakesha",
		},
		{
			eventName: "deployment",
			event: map[string]any{
				"deployment": map[string]any{
					"sha": "deployment-sha",
				},
			},
			sha: "deployment-sha",
		},
		{
			eventName: "release",
			event:     map[string]any{},
			sha:       "1234fakesha",
		},
		{
			eventName: "push",
			event: map[string]any{
				"after":   "push-sha",
				"deleted": false,
			},
			sha: "push-sha",
		},
		{
			eventName: "unknown",
			event:     map[string]any{},
			sha:       "1234fakesha",
		},
		{
			eventName: "no-event",
			event:     map[string]any{},
			sha:       "1234fakesha",
		},
	}

	for _, table := range tables {
		t.Run(table.eventName, func(t *testing.T) {
			ghc := &GithubContext{
				EventName: table.eventName,
				BaseRef:   "master",
				Event:     table.event,
			}

			ghc.SetSha(context.Background(), "/some/dir")

			assert.Equal(t, table.sha, ghc.Sha)
		})
	}
}
