// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"

	"code.gitea.io/actions-proto-go/ping/v1/pingv1connect"
	"code.gitea.io/actions-proto-go/runner/v1/runnerv1connect"
	"connectrpc.com/connect"
)

func getHTTPClient(endpoint string, insecure bool) *http.Client {
	if strings.HasPrefix(endpoint, "https://") && insecure {
		return &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		}
	}
	return http.DefaultClient
}

// New returns a new runner client.
func New(endpoint string, insecure bool, uuid, token, version string, opts ...connect.ClientOption) *HTTPClient {
	baseURL := strings.TrimRight(endpoint, "/") + "/api/actions"

	opts = append(opts, connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if uuid != "" {
				req.Header().Set(UUIDHeader, uuid)
			}
			if token != "" {
				req.Header().Set(TokenHeader, token)
			}
			// TODO: version will be removed from request header after Gitea 1.20 released.
			if version != "" {
				req.Header().Set(VersionHeader, version)
			}
			return next(ctx, req)
		}
	})))

	return &HTTPClient{
		PingServiceClient: pingv1connect.NewPingServiceClient(
			getHTTPClient(endpoint, insecure),
			baseURL,
			opts...,
		),
		RunnerServiceClient: runnerv1connect.NewRunnerServiceClient(
			getHTTPClient(endpoint, insecure),
			baseURL,
			opts...,
		),
		endpoint: endpoint,
		insecure: insecure,
	}
}

func (c *HTTPClient) Address() string {
	return c.endpoint
}

func (c *HTTPClient) Insecure() bool {
	return c.insecure
}

var _ Client = (*HTTPClient)(nil)

// An HTTPClient manages communication with the runner API.
type HTTPClient struct {
	pingv1connect.PingServiceClient
	runnerv1connect.RunnerServiceClient
	endpoint string
	insecure bool
}
