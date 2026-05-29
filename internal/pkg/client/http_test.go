// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package client

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetHTTPClientUsesProxyFromEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.example.com:8080")

	client := getHTTPClient("http://gitea.example.com", false)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)

	req, err := http.NewRequest(http.MethodGet, "http://gitea.example.com/api/actions/ping", nil)
	require.NoError(t, err)

	proxyURL, err := transport.Proxy(req)
	require.NoError(t, err)
	require.NotNil(t, proxyURL)
	require.Equal(t, "http://proxy.example.com:8080", proxyURL.String())
}
