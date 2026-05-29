// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"time"

	"code.gitea.io/actions-proto-go/ping/v1/pingv1connect"
	"code.gitea.io/actions-proto-go/runner/v1/runnerv1connect"
	"connectrpc.com/connect"
)

func getHTTPClient(endpoint string, insecure bool) *http.Client {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 10, // All requests go to one host; default is 2 which causes frequent reconnects.
		IdleConnTimeout:     90 * time.Second,
	}
	if strings.HasPrefix(endpoint, "https://") && insecure {
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}
	return &http.Client{Transport: transport}
}

// New returns a new runner client.
func New(endpoint string, insecure bool, uuid, token string, opts ...connect.ClientOption) *HTTPClient {
	baseURL := strings.TrimRight(endpoint, "/") + "/api/actions"

	opts = append(opts, connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if uuid != "" {
				req.Header().Set(UUIDHeader, uuid)
			}
			if token != "" {
				req.Header().Set(TokenHeader, token)
			}
			return next(ctx, req)
		}
	})))

	httpClient := getHTTPClient(endpoint, insecure)
	return &HTTPClient{
		PingServiceClient: pingv1connect.NewPingServiceClient(
			httpClient,
			baseURL,
			opts...,
		),
		RunnerServiceClient: runnerv1connect.NewRunnerServiceClient(
			httpClient,
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
