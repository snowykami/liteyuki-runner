// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build cgo
// +build cgo

package artifactcache

import _ "github.com/mattn/go-sqlite3"

var sqliteDriverName = "sqlite3"
