// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package labels

import (
	"fmt"
	"strings"
)

const (
	SchemeHost   = "host"
	SchemeDocker = "docker"
)

type Label struct {
	Name   string
	Schema string
	Arg    string
}

func Parse(str string) (*Label, error) {
	splits := strings.SplitN(str, ":", 3)
	label := &Label{
		Name:   splits[0],
		Schema: "host",
		Arg:    "",
	}
	if len(splits) >= 2 {
		label.Schema = splits[1]
	}
	if len(splits) >= 3 {
		label.Arg = splits[2]
	}
	if label.Schema != SchemeHost && label.Schema != SchemeDocker {
		return nil, fmt.Errorf("unsupported schema: %s", label.Schema)
	}
	return label, nil
}

type Labels []*Label

func (l Labels) RequireDocker() bool {
	for _, label := range l {
		if label.Schema == SchemeDocker {
			return true
		}
	}
	return false
}

func (l Labels) PickPlatform(runsOn []string) string {
	platforms := make(map[string]string, len(l))
	for _, label := range l {
		switch label.Schema {
		case SchemeDocker:
			// "//" will be ignored
			platforms[label.Name] = strings.TrimPrefix(label.Arg, "//")
		case SchemeHost:
			platforms[label.Name] = "-self-hosted"
		default:
			// It should not happen, because Parse has checked it.
			continue
		}
	}
	for _, v := range runsOn {
		if v, ok := platforms[v]; ok {
			return v
		}
	}

	// TODO: support multiple labels
	// like:
	//   ["ubuntu-22.04"] => "ubuntu:22.04"
	//   ["with-gpu"] => "linux:with-gpu"
	//   ["ubuntu-22.04", "with-gpu"] => "ubuntu:22.04_with-gpu"

	// return default.
	// So the runner receives a task with a label that the runner doesn't have,
	// it happens when the user have edited the label of the runner in the web UI.
	// TODO: it may be not correct, what if the runner is used as host mode only?
	return "docker.gitea.com/runner-images:ubuntu-latest"
}

func (l Labels) Names() []string {
	names := make([]string, 0, len(l))
	for _, label := range l {
		names = append(names, label.Name)
	}
	return names
}

func (l Labels) ToStrings() []string {
	ls := make([]string, 0, len(l))
	for _, label := range l {
		lbl := label.Name
		if label.Schema != "" {
			lbl += ":" + label.Schema
			if label.Arg != "" {
				lbl += ":" + label.Arg
			}
		}
		ls = append(ls, lbl)
	}
	return ls
}
