// Copyright 2026 The Gitea Authors. All rights reserved.
// Copyright 2026 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/model"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v4"
)

func TestRunScriptGroupTitle(t *testing.T) {
	sr := &stepRun{Step: &model.Step{Name: "Build"}}
	assert.Equal(t, "make build", sr.runScriptGroupTitle("make build"))
	assert.Equal(t, "echo one", sr.runScriptGroupTitle("  \techo one\necho two"))
	assert.Equal(t, "Build", sr.runScriptGroupTitle(""))

	sr = &stepRun{Step: &model.Step{ID: "s1"}}
	assert.Equal(t, "s1", sr.runScriptGroupTitle("\n  \n"))
}

func TestStepDeclaredEnvOrderPreservesYAML(t *testing.T) {
	raw := `id: s1
run: "echo 1"
env:
  GITHUB_TOKEN: tok
  PATH: /custom/bin
  MY_VAR: hello
`
	var step model.Step
	require.NoError(t, yaml.Unmarshal([]byte(raw), &step))
	assert.Equal(t, []string{"GITHUB_TOKEN", "PATH", "MY_VAR"}, stepDeclaredEnvKeysInOrder(&step))
}

func TestStepDeclaredEnvKeysInOrderEmpty(t *testing.T) {
	assert.Nil(t, stepDeclaredEnvKeysInOrder(nil))
	assert.Empty(t, stepDeclaredEnvKeysInOrder(&model.Step{}))
}

func TestStepDeclaredEnvKeysIgnoreYAMLMergeKey(t *testing.T) {
	doc := `
common: &common
  COMMON_A: a
  COMMON_B: b
step:
  env:
    LOCAL_BEFORE: before
    <<: *common
    COMMON_B: overridden
    LOCAL_AFTER: after
`
	var root struct {
		Step model.Step `yaml:"step"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(doc), &root))

	keys := stepDeclaredEnvKeysInOrder(&root.Step)
	assert.Equal(t, []string{"LOCAL_BEFORE", "COMMON_B", "LOCAL_AFTER"}, keys)
}

func TestPrintRunScriptActionDetailsGolden(t *testing.T) {
	raw := `id: s1
name: Build
run: |
  echo one
  echo two
shell: pwsh
env:
  PATH_PREFIX: /custom/bin
  GITHUB_TOKEN: tok
  GREETING: hello
`
	var step model.Step
	require.NoError(t, yaml.Unmarshal([]byte(raw), &step))

	buf := &bytes.Buffer{}
	logger := logrus.New()
	logger.SetOutput(buf)
	logger.SetLevel(logrus.InfoLevel)
	logger.SetFormatter(&jobLogFormatter{color: cyan})
	entry := logger.WithFields(logrus.Fields{"job": "j1"})
	ctx := common.WithLogger(context.Background(), entry)

	sr := &stepRun{
		Step:               &step,
		RunContext:         &RunContext{},
		shellCommand:       "pwsh -command . '{0}'",
		interpolatedScript: "echo one\necho two\n",
		env: map[string]string{
			"PATH_PREFIX":  "/custom/bin",
			"GITHUB_TOKEN": "tok",
			"GREETING":     "hello",
		},
	}

	sr.printRunScriptActionDetails(ctx)

	want := strings.Join([]string{
		"[j1]   | ::group::Run echo one",
		"[j1]   | echo one",
		"[j1]   | echo two",
		"[j1]   | shell: pwsh -command . '{0}'",
		"[j1]   | env:",
		"[j1]   |   PATH_PREFIX: /custom/bin",
		"[j1]   |   GREETING: hello",
		"[j1]   | ::endgroup::",
		"",
	}, "\n")
	assert.Equal(t, want, buf.String())
}

func TestPrintRunActionHeaderGolden(t *testing.T) {
	raw := `id: s1
uses: actions/checkout@v4
with:
  fetch-depth: "0"
  token: secret
env:
  CUSTOM: value
  GITHUB_TOKEN: tok
`
	var step model.Step
	require.NoError(t, yaml.Unmarshal([]byte(raw), &step))

	buf := &bytes.Buffer{}
	logger := logrus.New()
	logger.SetOutput(buf)
	logger.SetLevel(logrus.InfoLevel)
	logger.SetFormatter(&jobLogFormatter{color: cyan})
	entry := logger.WithFields(logrus.Fields{"job": "j1"})
	ctx := common.WithLogger(context.Background(), entry)

	printRunActionHeader(ctx, &step, map[string]string{"CUSTOM": "value", "GITHUB_TOKEN": "tok"}, &RunContext{})

	want := strings.Join([]string{
		"[j1]   | ::group::Run actions/checkout@v4",
		"[j1]   | with:",
		"[j1]   |   fetch-depth: 0",
		"[j1]   |   token: secret",
		"[j1]   | env:",
		"[j1]   |   CUSTOM: value",
		"",
	}, "\n")
	assert.Equal(t, want, buf.String())
}

func TestIsInternalEnvKey(t *testing.T) {
	for _, k := range []string{"PATH", "HOME", "CI", "GITHUB_TOKEN", "GITEA_ACTIONS", "RUNNER_OS", "INPUT_FOO"} {
		assert.True(t, isInternalEnvKey(k, false), k)
	}
	for _, k := range []string{"PATH_PREFIX", "MY_VAR", "GREETING", "HOMEPAGE"} {
		assert.False(t, isInternalEnvKey(k, false), k)
	}
	assert.True(t, isInternalEnvKey("path", true))
	assert.False(t, isInternalEnvKey("path", false))
}

func TestPrintColoredScriptLineCyan(t *testing.T) {
	f := &jobLogFormatter{color: cyan}
	entry := &logrus.Entry{
		Level:   logrus.InfoLevel,
		Message: "echo one",
		Data: logrus.Fields{
			"job":               "j1",
			rawOutputField:      true,
			scriptLineCyanField: true,
		},
	}
	buf := &bytes.Buffer{}
	f.printColored(buf, entry)
	assert.Equal(t, "\x1b[36m|\x1b[0m \x1b[36;1mecho one\x1b[0m", buf.String())
}
