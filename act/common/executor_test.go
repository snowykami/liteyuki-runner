// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWorkflow(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()

	// empty
	emptyWorkflow := NewPipelineExecutor()
	assert.NoError(emptyWorkflow(ctx)) //nolint:testifylint // pre-existing issue from nektos/act

	// error case
	errorWorkflow := NewErrorExecutor(errors.New("test error"))
	assert.Error(errorWorkflow(ctx)) //nolint:testifylint // pre-existing issue from nektos/act

	// multiple success case
	runcount := 0
	successWorkflow := NewPipelineExecutor(
		func(ctx context.Context) error {
			runcount++
			return nil
		},
		func(ctx context.Context) error {
			runcount++
			return nil
		})
	assert.NoError(successWorkflow(ctx)) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(2, runcount)
}

func TestNewConditionalExecutor(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()

	trueCount := 0
	falseCount := 0

	err := NewConditionalExecutor(func(ctx context.Context) bool {
		return false
	}, func(ctx context.Context) error {
		trueCount++
		return nil
	}, func(ctx context.Context) error {
		falseCount++
		return nil
	})(ctx)

	assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(0, trueCount)
	assert.Equal(1, falseCount)

	err = NewConditionalExecutor(func(ctx context.Context) bool {
		return true
	}, func(ctx context.Context) error {
		trueCount++
		return nil
	}, func(ctx context.Context) error {
		falseCount++
		return nil
	})(ctx)

	assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(1, trueCount)
	assert.Equal(1, falseCount)
}

func TestNewParallelExecutor(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()

	var count, activeCount, maxCount atomic.Int32
	emptyWorkflow := NewPipelineExecutor(func(ctx context.Context) error {
		count.Add(1)

		active := activeCount.Add(1)
		for {
			m := maxCount.Load()
			if active <= m || maxCount.CompareAndSwap(m, active) {
				break
			}
		}
		time.Sleep(2 * time.Second)
		activeCount.Add(-1)

		return nil
	})

	err := NewParallelExecutor(2, emptyWorkflow, emptyWorkflow, emptyWorkflow)(ctx)

	assert.Equal(int32(3), count.Load(), "should run all 3 executors")
	assert.Equal(int32(2), maxCount.Load(), "should run at most 2 executors in parallel")
	assert.NoError(err) //nolint:testifylint // pre-existing issue from nektos/act

	// Reset to test running the executor with 0 parallelism
	count.Store(0)
	activeCount.Store(0)
	maxCount.Store(0)

	errSingle := NewParallelExecutor(0, emptyWorkflow, emptyWorkflow, emptyWorkflow)(ctx)

	assert.Equal(int32(3), count.Load(), "should run all 3 executors")
	assert.Equal(int32(1), maxCount.Load(), "should run at most 1 executors in parallel")
	assert.NoError(errSingle)
}

func TestNewParallelExecutorEmpty(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()
	require.NoError(t, NewParallelExecutor(2)(ctx))

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewParallelExecutor(2)(canceledCtx)
	assert.ErrorIs(err, context.Canceled)
}

func TestNewParallelExecutorFailed(t *testing.T) {
	assert := assert.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	count := 0
	errorWorkflow := NewPipelineExecutor(func(ctx context.Context) error {
		count++
		return errors.New("fake error")
	})
	err := NewParallelExecutor(1, errorWorkflow)(ctx)
	assert.Equal(1, count)
	assert.ErrorIs(context.Canceled, err)
}

func TestNewParallelExecutorCanceled(t *testing.T) {
	assert := assert.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	errExpected := errors.New("fake error")

	var count atomic.Int32
	successWorkflow := NewPipelineExecutor(func(ctx context.Context) error {
		count.Add(1)
		return nil
	})
	errorWorkflow := NewPipelineExecutor(func(ctx context.Context) error {
		count.Add(1)
		return errExpected
	})
	err := NewParallelExecutor(3, errorWorkflow, successWorkflow, successWorkflow)(ctx)
	assert.Equal(int32(3), count.Load())
	assert.Error(errExpected, err) //nolint:testifylint // pre-existing issue from nektos/act
}
