// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gitea.com/gitea/runner/act/artifactcache"
	"gitea.com/gitea/runner/internal/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func emptyCfg() *config.Config { return &config.Config{} }

func TestRunner_registerCacheForTask(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifactcache")
	handler, err := artifactcache.StartHandler(dir, "127.0.0.1", 0, "", nil)
	require.NoError(t, err)
	defer handler.Close()

	r := &Runner{cfg: emptyCfg(), cacheHandler: handler}
	token := "run-token-123"
	unregister := r.registerCacheForTask(token, "owner/repo", nil)

	base := handler.ExternalURL() + "/_apis/artifactcache"
	probe := func() int {
		req, err := http.NewRequest(http.MethodGet, base+"/cache?keys=x&version=v", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}

	assert.NotEqual(t, http.StatusUnauthorized, probe(),
		"token should be accepted while task is registered")

	unregister()
	assert.Equal(t, http.StatusUnauthorized, probe(),
		"token must be rejected after the revoker runs")
}

func TestRunner_registerCacheForTask_NoOps(t *testing.T) {
	t.Run("nil cacheHandler", func(t *testing.T) {
		r := &Runner{cfg: emptyCfg()}
		unregister := r.registerCacheForTask("tok", "owner/repo", nil)
		require.NotNil(t, unregister)
		unregister()
	})

	t.Run("empty token", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "artifactcache")
		handler, err := artifactcache.StartHandler(dir, "127.0.0.1", 0, "", nil)
		require.NoError(t, err)
		defer handler.Close()

		r := &Runner{cfg: emptyCfg(), cacheHandler: handler}
		unregister := r.registerCacheForTask("", "owner/repo", nil)
		require.NotNil(t, unregister)
		unregister()
	})
}

// Locks in @actions/cache's wire protocol: bearer on reserve/upload/commit
// /find, no auth on the signed archiveLocation download.
func TestRunner_CacheFullFlow_MatchesToolkit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifactcache")
	handler, err := artifactcache.StartHandler(dir, "127.0.0.1", 0, "", nil)
	require.NoError(t, err)
	defer handler.Close()

	r := &Runner{cfg: emptyCfg(), cacheHandler: handler}
	token := "full-flow-token"
	unregister := r.registerCacheForTask(token, "owner/repo", nil)
	defer unregister()

	base := handler.ExternalURL() + "/_apis/artifactcache"
	do := func(method, url, contentType, contentRange, body string) *http.Response {
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if contentRange != "" {
			req.Header.Set("Content-Range", contentRange)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return resp
	}

	key := "toolkit-flow"
	version := "c19da02a2bd7e77277f1ac29ab45c09b7d46a4ee758284e26bb3045ad11d9d20"
	body := `hello-cache-body`

	// reserve
	resp := do(http.MethodPost, base+"/caches", "application/json", "",
		fmt.Sprintf(`{"key":"%s","version":"%s","cacheSize":%d}`, key, version, len(body)))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var reserved struct {
		CacheID uint64 `json:"cacheId"`
	}
	require.NoError(t, decodeJSON(resp, &reserved))
	require.NotZero(t, reserved.CacheID)

	// upload
	resp = do(http.MethodPatch, fmt.Sprintf("%s/caches/%d", base, reserved.CacheID),
		"application/octet-stream", fmt.Sprintf("bytes 0-%d/*", len(body)-1), body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// commit
	resp = do(http.MethodPost, fmt.Sprintf("%s/caches/%d", base, reserved.CacheID), "", "", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// find — @actions/cache always sends comma-separated keys here
	resp = do(http.MethodGet,
		fmt.Sprintf("%s/cache?keys=%s,fallback&version=%s", base, key, version), "", "", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var hit struct {
		ArchiveLocation string `json:"archiveLocation"`
		CacheKey        string `json:"cacheKey"`
	}
	require.NoError(t, decodeJSON(resp, &hit))
	require.Equal(t, key, hit.CacheKey)
	require.NotEmpty(t, hit.ArchiveLocation)

	// download — toolkit does NOT attach Authorization here; the signature
	// in the URL must be enough.
	dl, err := http.Get(hit.ArchiveLocation)
	require.NoError(t, err)
	defer dl.Body.Close()
	require.Equal(t, http.StatusOK, dl.StatusCode)
	got := make([]byte, 64)
	n, _ := dl.Body.Read(got)
	assert.Equal(t, body, string(got[:n]))
}

func decodeJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// End-to-end against a remote cache-server: token unknown → 401, register →
// reserve/upload/commit/find/download all OK, revoke → 401 again.
func TestRunner_ExternalCacheServer_RegisterRevoke(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "remote-cache")
	const secret = "shared-secret-for-tests"
	remote, err := artifactcache.StartHandler(dir, "127.0.0.1", 0, secret, nil)
	require.NoError(t, err)
	defer remote.Close()

	r := &Runner{cfg: &config.Config{Cache: config.Cache{
		ExternalServer: remote.ExternalURL(),
		ExternalSecret: secret,
	}}}

	token := "external-task-token"
	repo := "owner/repoX"
	base := remote.ExternalURL() + "/_apis/artifactcache"
	probe := func() int {
		req, _ := http.NewRequest(http.MethodGet, base+"/cache?keys=k&version=v", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}

	require.Equal(t, http.StatusUnauthorized, probe(),
		"token must be unknown to the remote server before registration")

	unregister := r.registerCacheForTask(token, repo, nil)
	require.NotEqual(t, http.StatusUnauthorized, probe(),
		"token must be accepted after registerCacheForTask")

	// Full reserve→upload→commit→find→download cycle, identical to what
	// @actions/cache does, against the remote (external) server.
	body := []byte("payload-from-task")
	reserveBody, _ := json.Marshal(&artifactcache.Request{Key: "ext-key", Version: "v", Size: int64(len(body))})
	req, _ := http.NewRequest(http.MethodPost, base+"/caches", bytes.NewReader(reserveBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var reserved struct {
		CacheID uint64 `json:"cacheId"`
	}
	require.NoError(t, decodeJSON(resp, &reserved))
	require.NotZero(t, reserved.CacheID)

	req, _ = http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/caches/%d", base, reserved.CacheID), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/*", len(body)-1))
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	req, _ = http.NewRequest(http.MethodPost, fmt.Sprintf("%s/caches/%d", base, reserved.CacheID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	req, _ = http.NewRequest(http.MethodGet, base+"/cache?keys=ext-key&version=v", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var hit struct {
		ArchiveLocation string `json:"archiveLocation"`
	}
	require.NoError(t, decodeJSON(resp, &hit))
	require.NotEmpty(t, hit.ArchiveLocation)

	dl, err := http.Get(hit.ArchiveLocation)
	require.NoError(t, err)
	defer dl.Body.Close()
	require.Equal(t, http.StatusOK, dl.StatusCode)

	unregister()
	assert.Equal(t, http.StatusUnauthorized, probe(),
		"token must be rejected after the revoker runs")
}
