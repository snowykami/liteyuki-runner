// Copyright 2021 The Gitea Authors. All rights reserved.
// Copyright 2021 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package model

type JobContext struct {
	Status    string `json:"status"`
	Container struct {
		ID      string `json:"id"`
		Network string `json:"network"`
	} `json:"container"`
	Services map[string]struct {
		ID string `json:"id"`
	} `json:"services"`
}
