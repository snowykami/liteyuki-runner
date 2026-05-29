// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Simple fast test that verifies max-parallel: 2 limits concurrency
func TestMaxParallel2Quick(t *testing.T) {
	ctx := context.Background()

	var currentRunning atomic.Int32
	var maxSimultaneous atomic.Int32

	executors := make([]Executor, 4)
	for i := range 4 {
		executors[i] = func(ctx context.Context) error {
			current := currentRunning.Add(1)

			// Update max if needed
			for {
				maxValue := maxSimultaneous.Load()
				if current <= maxValue || maxSimultaneous.CompareAndSwap(maxValue, current) {
					break
				}
			}

			time.Sleep(10 * time.Millisecond)
			currentRunning.Add(-1)
			return nil
		}
	}

	err := NewParallelExecutor(2, executors...)(ctx)

	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.LessOrEqual(t, maxSimultaneous.Load(), int32(2),
		"Should not exceed max-parallel: 2")
}

// Test that verifies max-parallel: 1 enforces sequential execution
func TestMaxParallel1Sequential(t *testing.T) {
	ctx := context.Background()

	var currentRunning atomic.Int32
	var maxSimultaneous atomic.Int32
	var executionOrder []int
	var orderMutex sync.Mutex

	executors := make([]Executor, 5)
	for i := range 5 {
		taskID := i
		executors[i] = func(ctx context.Context) error {
			current := currentRunning.Add(1)

			// Track execution order
			orderMutex.Lock()
			executionOrder = append(executionOrder, taskID)
			orderMutex.Unlock()

			// Update max if needed
			for {
				maxValue := maxSimultaneous.Load()
				if current <= maxValue || maxSimultaneous.CompareAndSwap(maxValue, current) {
					break
				}
			}

			time.Sleep(20 * time.Millisecond)
			currentRunning.Add(-1)
			return nil
		}
	}

	err := NewParallelExecutor(1, executors...)(ctx)

	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, int32(1), maxSimultaneous.Load(),
		"max-parallel: 1 should only run 1 task at a time")
	assert.Len(t, executionOrder, 5, "All 5 tasks should have executed")
}
