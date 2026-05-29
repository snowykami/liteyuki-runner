// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/common"

	"github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestDocker(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	client, err := GetDockerClient(ctx)
	require.NoError(t, err)
	defer client.Close()

	dockerBuild := NewDockerBuildExecutor(NewDockerBuildExecutorInput{
		ContextDir: "testdata",
		ImageTag:   "envmergetest",
	})

	err = dockerBuild(ctx)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	cr := &containerReference{
		cli: client,
		input: &NewContainerInput{
			Image: "envmergetest",
		},
	}
	env := map[string]string{
		"PATH":         "/usr/local/bin:/usr/bin:/usr/sbin:/bin:/sbin",
		"RANDOM_VAR":   "WITH_VALUE",
		"ANOTHER_VAR":  "",
		"CONFLICT_VAR": "I_EXIST_IN_MULTIPLE_PLACES",
	}

	envExecutor := cr.extractFromImageEnv(&env)
	err = envExecutor(ctx)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, map[string]string{
		"PATH":            "/usr/local/bin:/usr/bin:/usr/sbin:/bin:/sbin:/this/path/does/not/exists/anywhere:/this/either",
		"RANDOM_VAR":      "WITH_VALUE",
		"ANOTHER_VAR":     "",
		"SOME_RANDOM_VAR": "",
		"ANOTHER_ONE":     "BUT_I_HAVE_VALUE",
		"CONFLICT_VAR":    "I_EXIST_IN_MULTIPLE_PLACES",
	}, env)
}

type mockDockerClient struct {
	mobyclient.APIClient
	mock.Mock
}

func (m *mockDockerClient) ExecCreate(ctx context.Context, id string, opts mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(mobyclient.ExecCreateResult), args.Error(1)
}

func (m *mockDockerClient) ExecAttach(ctx context.Context, id string, opts mobyclient.ExecAttachOptions) (mobyclient.ExecAttachResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(mobyclient.ExecAttachResult), args.Error(1)
}

func (m *mockDockerClient) ExecInspect(ctx context.Context, execID string, opts mobyclient.ExecInspectOptions) (mobyclient.ExecInspectResult, error) {
	args := m.Called(ctx, execID, opts)
	return args.Get(0).(mobyclient.ExecInspectResult), args.Error(1)
}

func (m *mockDockerClient) ContainerWait(ctx context.Context, containerID string, opts mobyclient.ContainerWaitOptions) mobyclient.ContainerWaitResult {
	args := m.Called(ctx, containerID, opts)
	return args.Get(0).(mobyclient.ContainerWaitResult)
}

func (m *mockDockerClient) CopyToContainer(ctx context.Context, id string, options mobyclient.CopyToContainerOptions) (mobyclient.CopyToContainerResult, error) {
	args := m.Called(ctx, id, options)
	return args.Get(0).(mobyclient.CopyToContainerResult), args.Error(1)
}

type endlessReader struct {
	io.Reader
}

func (r endlessReader) Read(_ []byte) (n int, err error) {
	return 1, nil
}

type mockConn struct {
	net.Conn
	mock.Mock
}

func (m *mockConn) Write(b []byte) (n int, err error) {
	args := m.Called(b)
	return args.Int(0), args.Error(1)
}

func (m *mockConn) Close() (err error) {
	return nil
}

func TestDockerExecAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	conn := &mockConn{}
	conn.On("Write", mock.AnythingOfType("[]uint8")).Return(1, nil)

	client := &mockDockerClient{}
	client.On("ExecCreate", ctx, "123", mock.AnythingOfType("client.ExecCreateOptions")).Return(mobyclient.ExecCreateResult{ID: "id"}, nil)
	client.On("ExecAttach", ctx, "id", mock.AnythingOfType("client.ExecAttachOptions")).Return(mobyclient.ExecAttachResult{
		HijackedResponse: mobyclient.HijackedResponse{
			Conn:   conn,
			Reader: bufio.NewReader(endlessReader{}),
		},
	}, nil)

	cr := &containerReference{
		id:  "123",
		cli: client,
		input: &NewContainerInput{
			Image: "image",
		},
	}

	channel := make(chan error)

	go func() {
		channel <- cr.exec([]string{""}, map[string]string{}, "user", "workdir")(ctx)
	}()

	time.Sleep(500 * time.Millisecond)

	cancel()

	err := <-channel
	assert.ErrorIs(t, err, context.Canceled) //nolint:testifylint // pre-existing issue from nektos/act

	conn.AssertExpectations(t)
	client.AssertExpectations(t)
}

func TestDockerExecFailure(t *testing.T) {
	ctx := context.Background()

	conn := &mockConn{}

	client := &mockDockerClient{}
	client.On("ExecCreate", ctx, "123", mock.AnythingOfType("client.ExecCreateOptions")).Return(mobyclient.ExecCreateResult{ID: "id"}, nil)
	client.On("ExecAttach", ctx, "id", mock.AnythingOfType("client.ExecAttachOptions")).Return(mobyclient.ExecAttachResult{
		HijackedResponse: mobyclient.HijackedResponse{
			Conn:   conn,
			Reader: bufio.NewReader(strings.NewReader("output")),
		},
	}, nil)
	client.On("ExecInspect", ctx, "id", mobyclient.ExecInspectOptions{}).Return(mobyclient.ExecInspectResult{
		ExitCode: 1,
	}, nil)

	cr := &containerReference{
		id:  "123",
		cli: client,
		input: &NewContainerInput{
			Image: "image",
		},
	}

	err := cr.exec([]string{""}, map[string]string{}, "user", "workdir")(ctx)
	var exitErr ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, ExitCodeError(1), exitErr)
	assert.Equal(t, "Process completed with exit code 1.", err.Error())

	conn.AssertExpectations(t)
	client.AssertExpectations(t)
}

func TestDockerWaitFailure(t *testing.T) {
	ctx := context.Background()

	statusCh := make(chan container.WaitResponse, 1)
	statusCh <- container.WaitResponse{StatusCode: 2}
	errCh := make(chan error, 1)

	client := &mockDockerClient{}
	client.On("ContainerWait", ctx, "123", mobyclient.ContainerWaitOptions{Condition: container.WaitConditionNotRunning}).
		Return(mobyclient.ContainerWaitResult{
			Result: (<-chan container.WaitResponse)(statusCh),
			Error:  (<-chan error)(errCh),
		})

	cr := &containerReference{
		id:  "123",
		cli: client,
		input: &NewContainerInput{
			Image: "image",
		},
	}

	err := cr.wait()(ctx)
	var exitErr ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, ExitCodeError(2), exitErr)
	assert.Equal(t, "Process completed with exit code 2.", err.Error())

	client.AssertExpectations(t)
}

func TestDockerCopyTarStream(t *testing.T) {
	ctx := context.Background()

	client := &mockDockerClient{}
	client.On("CopyToContainer", ctx, "123", mock.MatchedBy(func(opts mobyclient.CopyToContainerOptions) bool {
		return opts.DestinationPath == "/" && opts.Content != nil
	})).Return(mobyclient.CopyToContainerResult{}, nil)
	client.On("CopyToContainer", ctx, "123", mock.MatchedBy(func(opts mobyclient.CopyToContainerOptions) bool {
		return opts.DestinationPath == "/var/run/act" && opts.Content != nil
	})).Return(mobyclient.CopyToContainerResult{}, nil)
	cr := &containerReference{
		id:  "123",
		cli: client,
		input: &NewContainerInput{
			Image: "image",
		},
	}

	_ = cr.CopyTarStream(ctx, "/var/run/act", &bytes.Buffer{})

	client.AssertExpectations(t)
}

func TestDockerCopyTarStreamErrorInCopyFiles(t *testing.T) {
	ctx := context.Background()

	merr := errors.New("Failure")

	client := &mockDockerClient{}
	client.On("CopyToContainer", ctx, "123", mock.MatchedBy(func(opts mobyclient.CopyToContainerOptions) bool {
		return opts.DestinationPath == "/" && opts.Content != nil
	})).Return(mobyclient.CopyToContainerResult{}, merr)
	cr := &containerReference{
		id:  "123",
		cli: client,
		input: &NewContainerInput{
			Image: "image",
		},
	}

	err := cr.CopyTarStream(ctx, "/var/run/act", &bytes.Buffer{})
	assert.ErrorIs(t, err, merr) //nolint:testifylint // pre-existing issue from nektos/act

	client.AssertExpectations(t)
}

func TestDockerCopyTarStreamErrorInMkdir(t *testing.T) {
	ctx := context.Background()

	merr := errors.New("Failure")

	client := &mockDockerClient{}
	client.On("CopyToContainer", ctx, "123", mock.MatchedBy(func(opts mobyclient.CopyToContainerOptions) bool {
		return opts.DestinationPath == "/" && opts.Content != nil
	})).Return(mobyclient.CopyToContainerResult{}, nil)
	client.On("CopyToContainer", ctx, "123", mock.MatchedBy(func(opts mobyclient.CopyToContainerOptions) bool {
		return opts.DestinationPath == "/var/run/act" && opts.Content != nil
	})).Return(mobyclient.CopyToContainerResult{}, merr)
	cr := &containerReference{
		id:  "123",
		cli: client,
		input: &NewContainerInput{
			Image: "image",
		},
	}

	err := cr.CopyTarStream(ctx, "/var/run/act", &bytes.Buffer{})
	assert.ErrorIs(t, err, merr) //nolint:testifylint // pre-existing issue from nektos/act

	client.AssertExpectations(t)
}

// TestDockerCopyToSymlinkPath is a regression test for gitea/runner#981. Most base images
// symlink /var/run to /run, so copying into /var/run/act traverses that symlink. The broken
// docker 29.5.1 daemon fails the extraction with "mkdirat var/run: file exists" (fixed in
// 29.5.2). Running against the daemon shipped in the dind image, this catches a bad bump.
func TestDockerCopyToSymlinkPath(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	rc := NewContainer(&NewContainerInput{
		Image:      "alpine:latest",
		Entrypoint: []string{"sleep", "30"},
		Name:       "act-test-symlink-" + time.Now().Format("20060102150405.000000"),
		AutoRemove: true,
	})
	require.NoError(t, rc.Pull(false)(ctx))
	require.NoError(t, rc.Create(nil, nil)(ctx))
	require.NoError(t, rc.Start(false)(ctx))
	t.Cleanup(func() {
		_ = rc.Remove()(ctx)
		_ = rc.Close()(ctx)
	})

	// CopyTarStream first creates the destination directory by extracting a tar at "/",
	// which makes the daemon mkdir var, then var/run (the symlink), then act — the exact
	// step that fails on the broken daemon.
	err := rc.CopyTarStream(ctx, "/var/run/act", &bytes.Buffer{})
	require.NoError(t, err)
}

// Type assert containerReference implements ExecutionsEnvironment
var _ ExecutionsEnvironment = &containerReference{}

func TestCheckVolumes(t *testing.T) {
	testCases := []struct {
		desc          string
		validVolumes  []string
		binds         []string
		expectedBinds []string
	}{
		{
			desc:         "match all volumes",
			validVolumes: []string{"**"},
			binds: []string{
				"shared_volume:/shared_volume",
				"/home/test/data:/test_data",
				"/etc/conf.d/base.json:/config/base.json",
				"sql_data:/sql_data",
				"/secrets/keys:/keys",
			},
			expectedBinds: []string{
				"shared_volume:/shared_volume",
				"/home/test/data:/test_data",
				"/etc/conf.d/base.json:/config/base.json",
				"sql_data:/sql_data",
				"/secrets/keys:/keys",
			},
		},
		{
			desc:         "no volumes can be matched",
			validVolumes: []string{},
			binds: []string{
				"shared_volume:/shared_volume",
				"/home/test/data:/test_data",
				"/etc/conf.d/base.json:/config/base.json",
				"sql_data:/sql_data",
				"/secrets/keys:/keys",
			},
			expectedBinds: []string{},
		},
		{
			desc: "only allowed volumes can be matched",
			validVolumes: []string{
				"shared_volume",
				"/home/test/data",
				"/etc/conf.d/*.json",
			},
			binds: []string{
				"shared_volume:/shared_volume",
				"/home/test/data:/test_data",
				"/etc/conf.d/base.json:/config/base.json",
				"sql_data:/sql_data",
				"/secrets/keys:/keys",
			},
			expectedBinds: []string{
				"shared_volume:/shared_volume",
				"/home/test/data:/test_data",
				"/etc/conf.d/base.json:/config/base.json",
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			logger, _ := test.NewNullLogger()
			ctx := common.WithLogger(context.Background(), logger)
			cr := &containerReference{
				input: &NewContainerInput{
					ValidVolumes: tc.validVolumes,
				},
			}
			_, hostConf := cr.sanitizeConfig(ctx, &container.Config{}, &container.HostConfig{Binds: tc.binds})
			assert.Equal(t, tc.expectedBinds, hostConf.Binds)
		})
	}
}

func TestCheckVolumesRejectsEscapingHostPaths(t *testing.T) {
	logger, _ := test.NewNullLogger()
	ctx := common.WithLogger(context.Background(), logger)

	base := t.TempDir()
	allowed := filepath.Join(base, "allowed")
	denied := filepath.Join(base, "denied")
	require.NoError(t, os.MkdirAll(allowed, 0o700))
	require.NoError(t, os.MkdirAll(denied, 0o700))

	cr := &containerReference{
		input: &NewContainerInput{
			ValidVolumes: []string{filepath.Join(allowed, "**")},
		},
	}

	escapingPath := allowed + string(filepath.Separator) + ".." + string(filepath.Separator) + "denied"
	_, hostConf := cr.sanitizeConfig(ctx, &container.Config{}, &container.HostConfig{
		Binds: []string{escapingPath + ":/mnt"},
	})
	assert.Empty(t, hostConf.Binds)

	linkPath := filepath.Join(allowed, "link")
	if err := os.Symlink(denied, linkPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	_, hostConf = cr.sanitizeConfig(ctx, &container.Config{}, &container.HostConfig{
		Binds: []string{linkPath + ":/mnt"},
	})
	assert.Empty(t, hostConf.Binds)

	_, hostConf = cr.sanitizeConfig(ctx, &container.Config{}, &container.HostConfig{
		Binds: []string{filepath.Join(linkPath, "missing") + ":/mnt"},
	})
	assert.Empty(t, hostConf.Binds)
}
