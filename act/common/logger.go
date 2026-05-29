// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"context"

	"github.com/sirupsen/logrus"
)

type loggerContextKey string

const loggerContextKeyVal = loggerContextKey("logrus.FieldLogger")

// Logger returns the appropriate logger for current context
func Logger(ctx context.Context) logrus.FieldLogger {
	val := ctx.Value(loggerContextKeyVal)
	if val != nil {
		if logger, ok := val.(logrus.FieldLogger); ok {
			return logger
		}
	}
	return logrus.StandardLogger()
}

// WithLogger adds a value to the context for the logger
func WithLogger(ctx context.Context, logger logrus.FieldLogger) context.Context {
	return context.WithValue(ctx, loggerContextKeyVal, logger)
}

type loggerHookKey string

const loggerHookKeyVal = loggerHookKey("logrus.Hook")

// LoggerHook returns the appropriate logger hook for current context
// the hook affects job logger, not global logger
func LoggerHook(ctx context.Context) logrus.Hook {
	val := ctx.Value(loggerHookKeyVal)
	if val != nil {
		if hook, ok := val.(logrus.Hook); ok {
			return hook
		}
	}
	return nil
}

// WithLoggerHook adds a value to the context for the logger hook
func WithLoggerHook(ctx context.Context, hook logrus.Hook) context.Context {
	return context.WithValue(ctx, loggerHookKeyVal, hook)
}
