// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
)

// TestStepCloneIsolatesMutableFields guards the parallel-matrix race fix: combinations share the
// job's *Step, and Clone() must hand each a copy whose If/Env nodes and With map can be mutated
// independently. A shallow copy would share Env.Content's backing array (and the With map) and
// leak writes across combinations.
func TestStepCloneIsolatesMutableFields(t *testing.T) {
	var orig Step
	require.NoError(t, yaml.Unmarshal([]byte("if: ${{ env.X == 'a' }}\nenv:\n  KEY: original\nwith:\n  arg: original\n"), &orig))
	require.Len(t, orig.Env.Content, 2) // [key, value]

	clone := orig.Clone()
	clone.If.Value = "changed"
	clone.Env.Content[1].Value = "changed"
	clone.With["arg"] = "changed"

	assert.Equal(t, "${{ env.X == 'a' }}", orig.If.Value, "If must not be shared with the clone")
	assert.Equal(t, "original", orig.Env.Content[1].Value, "Env nodes must not be shared with the clone")
	assert.Equal(t, "original", orig.With["arg"], "With map must not be shared with the clone")
}

func TestReadWorkflow_ScheduleEvent(t *testing.T) {
	yaml := `
name: local-action-docker-url
on:
  schedule:
    - cron: '30 5 * * 1,3'
    - cron: '30 5 * * 2,4'

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	schedules := workflow.OnEvent("schedule")
	assert.Len(t, schedules, 2)

	newSchedules := workflow.OnSchedule()
	assert.Len(t, newSchedules, 2)

	assert.Equal(t, "30 5 * * 1,3", newSchedules[0])
	assert.Equal(t, "30 5 * * 2,4", newSchedules[1])

	yaml = `
name: local-action-docker-url
on:
  schedule:
    test: '30 5 * * 1,3'

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	newSchedules = workflow.OnSchedule()
	assert.Empty(t, newSchedules)

	yaml = `
name: local-action-docker-url
on:
  schedule:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	newSchedules = workflow.OnSchedule()
	assert.Empty(t, newSchedules)

	yaml = `
name: local-action-docker-url
on: [push, tag]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	newSchedules = workflow.OnSchedule()
	assert.Empty(t, newSchedules)
}

func TestReadWorkflow_StringEvent(t *testing.T) {
	yaml := `
name: local-action-docker-url
on: push

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	assert.Len(t, workflow.On(), 1)
	assert.Contains(t, workflow.On(), "push")
}

func TestReadWorkflow_ListEvent(t *testing.T) {
	yaml := `
name: local-action-docker-url
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

	assert.Len(t, workflow.On(), 2)
	assert.Contains(t, workflow.On(), "push")
	assert.Contains(t, workflow.On(), "pull_request")
}

func TestReadWorkflow_MapEvent(t *testing.T) {
	yaml := `
name: local-action-docker-url
on:
  push:
    branches:
    - master
  pull_request:
    branches:
    - master

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.On(), 2)
	assert.Contains(t, workflow.On(), "push")
	assert.Contains(t, workflow.On(), "pull_request")
}

func TestReadWorkflow_RunsOnLabels(t *testing.T) {
	yaml := `
name: local-action-docker-url

jobs:
  test:
    container: nginx:latest
    runs-on:
      labels: ubuntu-latest
    steps:
    - uses: ./actions/docker-url`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed")                     //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].RunsOn(), []string{"ubuntu-latest"}) //nolint:testifylint // pre-existing issue from nektos/act
}

func TestReadWorkflow_RunsOnLabelsWithGroup(t *testing.T) {
	yaml := `
name: local-action-docker-url

jobs:
  test:
    container: nginx:latest
    runs-on:
      labels: [ubuntu-latest]
      group: linux
    steps:
    - uses: ./actions/docker-url`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed")                              //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].RunsOn(), []string{"ubuntu-latest", "linux"}) //nolint:testifylint // pre-existing issue from nektos/act
}

func TestReadWorkflow_StringContainer(t *testing.T) {
	yaml := `
name: local-action-docker-url

jobs:
  test:
    container: nginx:latest
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
  test2:
    container:
      image: nginx:latest
      env:
        foo: bar
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 2)
	assert.Contains(t, workflow.Jobs["test"].Container().Image, "nginx:latest")
	assert.Contains(t, workflow.Jobs["test2"].Container().Image, "nginx:latest")
	assert.Contains(t, workflow.Jobs["test2"].Container().Env["foo"], "bar")
}

func TestReadWorkflow_ObjectContainer(t *testing.T) {
	yaml := `
name: local-action-docker-url

jobs:
  test:
    container:
      image: r.example.org/something:latest
      credentials:
        username: registry-username
        password: registry-password
      env:
        HOME: /home/user
      volumes:
        - my_docker_volume:/volume_mount
        - /data/my_data
        - /source/directory:/destination/directory
    runs-on: ubuntu-latest
    steps:
    - uses: ./actions/docker-url
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 1)

	container := workflow.GetJob("test").Container()

	assert.Contains(t, container.Image, "r.example.org/something:latest")
	assert.Contains(t, container.Env["HOME"], "/home/user")
	assert.Contains(t, container.Credentials["username"], "registry-username")
	assert.Contains(t, container.Credentials["password"], "registry-password")
	assert.ElementsMatch(t, container.Volumes, []string{
		"my_docker_volume:/volume_mount",
		"/data/my_data",
		"/source/directory:/destination/directory",
	})
}

func TestReadWorkflow_JobTypes(t *testing.T) {
	yaml := `
name: invalid job definition

jobs:
  default-job:
    runs-on: ubuntu-latest
    steps:
      - run: echo
  remote-reusable-workflow-yml:
    uses: remote/repo/some/path/to/workflow.yml@main
  remote-reusable-workflow-yaml:
    uses: remote/repo/some/path/to/workflow.yaml@main
  remote-reusable-workflow-custom-path:
    uses: remote/repo/path/to/workflow.yml@main
  local-reusable-workflow-yml:
    uses: ./some/path/to/workflow.yml
  local-reusable-workflow-yaml:
    uses: ./some/path/to/workflow.yaml
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 6)

	jobType, err := workflow.Jobs["default-job"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeDefault, jobType)

	jobType, err = workflow.Jobs["remote-reusable-workflow-yml"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeReusableWorkflowRemote, jobType)

	jobType, err = workflow.Jobs["remote-reusable-workflow-yaml"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeReusableWorkflowRemote, jobType)

	jobType, err = workflow.Jobs["remote-reusable-workflow-custom-path"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeReusableWorkflowRemote, jobType)

	jobType, err = workflow.Jobs["local-reusable-workflow-yml"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeReusableWorkflowLocal, jobType)

	jobType, err = workflow.Jobs["local-reusable-workflow-yaml"].Type()
	assert.Equal(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, JobTypeReusableWorkflowLocal, jobType)
}

func TestReadWorkflow_JobTypes_InvalidPath(t *testing.T) {
	yaml := `
name: invalid job definition

jobs:
  remote-reusable-workflow-missing-version:
    uses: remote/repo/some/path/to/workflow.yml
  remote-reusable-workflow-bad-extension:
    uses: remote/repo/some/path/to/workflow.json
  local-reusable-workflow-bad-extension:
    uses: ./some/path/to/workflow.json
  local-reusable-workflow-bad-path:
    uses: some/path/to/workflow.yaml
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 4)

	jobType, err := workflow.Jobs["remote-reusable-workflow-missing-version"].Type()
	assert.Equal(t, JobTypeInvalid, jobType)
	assert.NotEqual(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act

	jobType, err = workflow.Jobs["remote-reusable-workflow-bad-extension"].Type()
	assert.Equal(t, JobTypeInvalid, jobType)
	assert.NotEqual(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act

	jobType, err = workflow.Jobs["local-reusable-workflow-bad-extension"].Type()
	assert.Equal(t, JobTypeInvalid, jobType)
	assert.NotEqual(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act

	jobType, err = workflow.Jobs["local-reusable-workflow-bad-path"].Type()
	assert.Equal(t, JobTypeInvalid, jobType)
	assert.NotEqual(t, nil, err) //nolint:testifylint // pre-existing issue from nektos/act
}

func TestReadWorkflow_StepsTypes(t *testing.T) {
	yaml := `
name: invalid step definition

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: test1
        uses: actions/checkout@v2
        run: echo
      - name: test2
        run: echo
      - name: test3
        uses: actions/checkout@v2
      - name: test4
        uses: docker://nginx:latest
      - name: test5
        uses: ./local-action
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 1)
	assert.Len(t, workflow.Jobs["test"].Steps, 5)
	assert.Equal(t, workflow.Jobs["test"].Steps[0].Type(), StepTypeInvalid)          //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].Steps[1].Type(), StepTypeRun)              //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].Steps[2].Type(), StepTypeUsesActionRemote) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].Steps[3].Type(), StepTypeUsesDockerURL)    //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, workflow.Jobs["test"].Steps[4].Type(), StepTypeUsesActionLocal)  //nolint:testifylint // pre-existing issue from nektos/act
}

// See: https://docs.github.com/en/actions/reference/workflow-syntax-for-github-actions#jobsjob_idoutputs
func TestReadWorkflow_JobOutputs(t *testing.T) {
	yaml := `
name: job outputs definition

jobs:
  test1:
    runs-on: ubuntu-latest
    steps:
      - id: test1_1
        run: |
          echo "::set-output name=a_key::some-a_value"
          echo "::set-output name=b-key::some-b-value"
    outputs:
      some_a_key: ${{ steps.test1_1.outputs.a_key }}
      some-b-key: ${{ steps.test1_1.outputs.b-key }}

  test2:
    runs-on: ubuntu-latest
    needs:
      - test1
    steps:
      - name: test2_1
        run: |
          echo "${{ needs.test1.outputs.some_a_key }}"
          echo "${{ needs.test1.outputs.some-b-key }}"
`

	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Len(t, workflow.Jobs, 2)

	assert.Len(t, workflow.Jobs["test1"].Steps, 1)
	assert.Equal(t, StepTypeRun, workflow.Jobs["test1"].Steps[0].Type())
	assert.Equal(t, "test1_1", workflow.Jobs["test1"].Steps[0].ID)
	assert.Len(t, workflow.Jobs["test1"].Outputs, 2)
	assert.Contains(t, workflow.Jobs["test1"].Outputs, "some_a_key")
	assert.Contains(t, workflow.Jobs["test1"].Outputs, "some-b-key")
	assert.Equal(t, "${{ steps.test1_1.outputs.a_key }}", workflow.Jobs["test1"].Outputs["some_a_key"])
	assert.Equal(t, "${{ steps.test1_1.outputs.b-key }}", workflow.Jobs["test1"].Outputs["some-b-key"])
}

func TestReadWorkflow_Strategy(t *testing.T) {
	w, err := NewWorkflowPlanner("testdata/strategy/push.yml", true)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	p, err := w.PlanJob("strategy-only-max-parallel")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	assert.Equal(t, len(p.Stages), 1)         //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, len(p.Stages[0].Runs), 1) //nolint:testifylint // pre-existing issue from nektos/act

	wf := p.Stages[0].Runs[0].Workflow

	job := wf.Jobs["strategy-only-max-parallel"]
	matrixes, err := job.GetMatrixes()
	assert.NoError(t, err)                          //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, matrixes, []map[string]any{{}}) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, job.Matrix(), map[string][]any(nil))
	assert.Equal(t, job.Strategy.MaxParallel, 2) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, job.Strategy.FailFast, true) //nolint:testifylint // pre-existing issue from nektos/act

	job = wf.Jobs["strategy-only-fail-fast"]
	matrixes, err = job.GetMatrixes()
	assert.NoError(t, err)                          //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, matrixes, []map[string]any{{}}) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, job.Matrix(), map[string][]any(nil))
	assert.Equal(t, job.Strategy.MaxParallel, 4)  //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, job.Strategy.FailFast, false) //nolint:testifylint // pre-existing issue from nektos/act

	job = wf.Jobs["strategy-no-matrix"]
	matrixes, err = job.GetMatrixes()
	assert.NoError(t, err)                          //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, matrixes, []map[string]any{{}}) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, job.Matrix(), map[string][]any(nil))
	assert.Equal(t, job.Strategy.MaxParallel, 2)  //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, job.Strategy.FailFast, false) //nolint:testifylint // pre-existing issue from nektos/act

	job = wf.Jobs["strategy-all"]
	matrixes, err = job.GetMatrixes()
	assert.NoError(t, err)    //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, matrixes, //nolint:testifylint // pre-existing issue from nektos/act
		[]map[string]any{
			{"datacenter": "site-c", "node-version": "14.x", "site": "staging"},
			{"datacenter": "site-c", "node-version": "16.x", "site": "staging"},
			{"datacenter": "site-d", "node-version": "16.x", "site": "staging"},
			{"php-version": 5.4},
			{"datacenter": "site-a", "node-version": "10.x", "site": "prod"},
			{"datacenter": "site-b", "node-version": "12.x", "site": "dev"},
		},
	)
	assert.Equal(t, job.Matrix(), //nolint:testifylint // pre-existing issue from nektos/act
		map[string][]any{
			"datacenter": {"site-c", "site-d"},
			"exclude": {
				map[string]any{"datacenter": "site-d", "node-version": "14.x", "site": "staging"},
			},
			"include": {
				map[string]any{"php-version": 5.4},
				map[string]any{"datacenter": "site-a", "node-version": "10.x", "site": "prod"},
				map[string]any{"datacenter": "site-b", "node-version": "12.x", "site": "dev"},
			},
			"node-version": {"14.x", "16.x"},
			"site":         {"staging"},
		},
	)
	assert.Equal(t, job.Strategy.MaxParallel, 2)  //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, job.Strategy.FailFast, false) //nolint:testifylint // pre-existing issue from nektos/act
}

func TestStep_ShellCommand(t *testing.T) {
	tests := []struct {
		shell string
		want  string
	}{
		{"pwsh -v '. {0}'", "pwsh -v '. {0}'"},
		{"pwsh", "pwsh -command . '{0}'"},
		{"powershell", "powershell -command . '{0}'"},
	}
	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			got := (&Step{Shell: tt.shell}).ShellCommand()
			assert.Equal(t, got, tt.want) //nolint:testifylint // pre-existing issue from nektos/act
		})
	}
}

func TestReadWorkflow_WorkflowDispatchConfig(t *testing.T) {
	yaml := `
    name: local-action-docker-url
    `
	workflow, err := ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch := workflow.WorkflowDispatchConfig()
	assert.Nil(t, workflowDispatch)

	yaml = `
    name: local-action-docker-url
    on: push
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.Nil(t, workflowDispatch)

	yaml = `
    name: local-action-docker-url
    on: workflow_dispatch
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.NotNil(t, workflowDispatch)
	assert.Nil(t, workflowDispatch.Inputs)

	yaml = `
    name: local-action-docker-url
    on: [push, pull_request]
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.Nil(t, workflowDispatch)

	yaml = `
    name: local-action-docker-url
    on: [push, workflow_dispatch]
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.NotNil(t, workflowDispatch)
	assert.Nil(t, workflowDispatch.Inputs)

	yaml = `
    name: local-action-docker-url
    on:
        - push
        - workflow_dispatch
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.NotNil(t, workflowDispatch)
	assert.Nil(t, workflowDispatch.Inputs)

	yaml = `
    name: local-action-docker-url
    on:
        push:
        pull_request:
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.Nil(t, workflowDispatch)

	yaml = `
    name: local-action-docker-url
    on:
        push:
        pull_request:
        workflow_dispatch:
            inputs:
                logLevel:
                    description: 'Log level'
                    required: true
                    default: 'warning'
                    type: choice
                    options:
                    - info
                    - warning
                    - debug
    `
	workflow, err = ReadWorkflow(strings.NewReader(yaml))
	assert.NoError(t, err, "read workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act
	workflowDispatch = workflow.WorkflowDispatchConfig()
	assert.NotNil(t, workflowDispatch)
	assert.Equal(t, WorkflowDispatchInput{
		Default:     "warning",
		Description: "Log level",
		Options: []string{
			"info",
			"warning",
			"debug",
		},
		Required: true,
		Type:     "choice",
	}, workflowDispatch.Inputs["logLevel"])
}

func TestStep_UsesHash(t *testing.T) {
	type fields struct {
		Uses string
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "regular",
			fields: fields{
				Uses: "https://gitea.com/testa/testb@v3",
			},
			want: "ae437878e9f285bd7518c58664f9fabbb12d05feddd7169c01702a2a14322aa8",
		},
		{
			name: "empty",
			fields: fields{
				Uses: "",
			},
			want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Step{
				Uses: tt.fields.Uses,
			}
			assert.Equalf(t, tt.want, s.UsesHash(), "UsesHash()")
		})
	}
}

func TestNormalizeMatrixValue(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		value      any
		wantResult []any
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "array_values_pass_through",
			key:        "version",
			value:      []any{"1.0", "2.0", "3.0"},
			wantResult: []any{"1.0", "2.0", "3.0"},
			wantErr:    false,
		},
		{
			name:       "string_scalar_wrapped",
			key:        "os",
			value:      "ubuntu-latest",
			wantResult: []any{"ubuntu-latest"},
			wantErr:    false,
		},
		{
			name:       "template_expression_wrapped",
			key:        "version",
			value:      "${{ fromJson(needs.setup.outputs.versions) }}",
			wantResult: []any{"${{ fromJson(needs.setup.outputs.versions) }}"},
			wantErr:    false,
		},
		{
			name:       "integer_scalar_wrapped",
			key:        "count",
			value:      42,
			wantResult: []any{42},
			wantErr:    false,
		},
		{
			name:       "float_scalar_wrapped",
			key:        "factor",
			value:      3.14,
			wantResult: []any{3.14},
			wantErr:    false,
		},
		{
			name:       "bool_scalar_wrapped",
			key:        "enabled",
			value:      true,
			wantResult: []any{true},
			wantErr:    false,
		},
		{
			name:       "nil_scalar_wrapped",
			key:        "optional",
			value:      nil,
			wantResult: []any{nil},
			wantErr:    false,
		},
		{
			name:    "nested_map_returns_error",
			key:     "config",
			value:   map[string]any{"nested": "value"},
			wantErr: true,
			errMsg:  "has invalid nested object value",
		},
		{
			name:       "empty_array_passes_through",
			key:        "empty",
			value:      []any{},
			wantResult: []any{},
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := normalizeMatrixValue(tt.key, tt.value)

			if tt.wantErr {
				assert.Error(t, err, "should return error") //nolint:testifylint // pre-existing issue from nektos/act
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err, "should not return error") //nolint:testifylint // pre-existing issue from nektos/act
				assert.Equal(t, tt.wantResult, result, "result should match expected")
			}
		})
	}
}

func TestJobMatrix(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		wantLen int
		checkFn func(*testing.T, map[string][]any)
	}{
		{
			name: "matrix_with_arrays",
			yaml: `
name: test
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        os: [ubuntu-latest, windows-latest]
        version: [1.18, 1.19]
    steps:
      - run: echo test
`,
			wantErr: false,
			wantLen: 2,
			checkFn: func(t *testing.T, m map[string][]any) {
				assert.Equal(t, []any{"ubuntu-latest", "windows-latest"}, m["os"])
				assert.Equal(t, []any{1.18, 1.19}, m["version"])
			},
		},
		{
			name: "matrix_with_scalar_values",
			yaml: `
name: test
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        os: ubuntu-latest
        version: 1.19
    steps:
      - run: echo test
`,
			wantErr: false,
			wantLen: 2,
			checkFn: func(t *testing.T, m map[string][]any) {
				assert.Equal(t, []any{"ubuntu-latest"}, m["os"])
				assert.Equal(t, []any{1.19}, m["version"])
			},
		},
		{
			name: "matrix_with_template_expression",
			yaml: `
name: test
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        versions: ${{ fromJson(needs.setup.outputs.versions) }}
    steps:
      - run: echo test
`,
			wantErr: false,
			wantLen: 1,
			checkFn: func(t *testing.T, m map[string][]any) {
				assert.Equal(t, []any{"${{ fromJson(needs.setup.outputs.versions) }}"}, m["versions"])
			},
		},
		{
			name: "matrix_mixed_arrays_and_scalars",
			yaml: `
name: test
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        os: [ubuntu-latest, windows-latest]
        version: 1.19
        node: [14, 16]
    steps:
      - run: echo test
`,
			wantErr: false,
			wantLen: 3,
			checkFn: func(t *testing.T, m map[string][]any) {
				assert.Equal(t, []any{"ubuntu-latest", "windows-latest"}, m["os"])
				assert.Equal(t, []any{1.19}, m["version"])
				assert.Equal(t, []any{14, 16}, m["node"])
			},
		},
		{
			name: "empty_matrix",
			yaml: `
name: test
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo test
`,
			wantErr: false,
			wantLen: 0,
			checkFn: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflow, err := ReadWorkflow(strings.NewReader(tt.yaml))
			assert.NoError(t, err, "reading workflow should succeed") //nolint:testifylint // pre-existing issue from nektos/act

			job := workflow.GetJob("build")
			if job == nil {
				// For empty matrix test
				if tt.wantLen == 0 {
					return
				}
				t.Fatal("job not found")
			}

			matrix := job.Matrix()

			if tt.wantErr {
				assert.Nil(t, matrix, "matrix should be nil on error")
			} else {
				if tt.wantLen == 0 {
					assert.Nil(t, matrix, "matrix should be nil for jobs without strategy")
				} else {
					assert.NotNil(t, matrix, "matrix should not be nil")
					assert.Len(t, matrix, tt.wantLen, "matrix should have expected number of keys")
					if tt.checkFn != nil {
						tt.checkFn(t, matrix)
					}
				}
			}
		})
	}
}

func TestJobMatrixValidation(t *testing.T) {
	// This test verifies that invalid nested map values are caught
	t.Run("matrix_with_nested_map_fails", func(t *testing.T) {
		// Manually construct a job with a problematic matrix containing a nested map
		job := &Job{
			Strategy: &Strategy{
				RawMatrix: yaml.Node{
					Kind: yaml.MappingNode,
					Content: []*yaml.Node{
						{Kind: yaml.ScalarNode, Tag: "!!str", Value: "config"},
						{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "nested"},
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "value"},
						}},
					},
				},
			},
		}

		// Attempt to get matrix
		matrix := job.Matrix()

		// Should return nil due to validation error
		assert.Nil(t, matrix, "matrix with nested map should return nil")
	})
}
