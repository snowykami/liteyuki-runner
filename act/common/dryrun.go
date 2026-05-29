// Copyright 2020 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"context"
)

type dryrunContextKey string

const dryrunContextKeyVal = dryrunContextKey("dryrun")

// Dryrun returns true if the current context is dryrun
func Dryrun(ctx context.Context) bool {
	val := ctx.Value(dryrunContextKeyVal)
	if val != nil {
		if dryrun, ok := val.(bool); ok {
			return dryrun
		}
	}
	return false
}

// WithDryrun adds a value to the context for dryrun
func WithDryrun(ctx context.Context, dryrun bool) context.Context {
	return context.WithValue(ctx, dryrunContextKeyVal, dryrun)
}
