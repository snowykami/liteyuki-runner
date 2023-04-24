// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !cgo
// +build !cgo

package artifactcache

import _ "modernc.org/sqlite"

var sqliteDriverName = "sqlite"
