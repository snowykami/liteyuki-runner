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

// TestMaxParallelJobExecution tests actual job execution with max-parallel
func TestMaxParallelJobExecution(t *testing.T) {
	t.Run("MaxParallel=1 Sequential", func(t *testing.T) {
		var currentRunning atomic.Int32
		var maxConcurrent int32
		var executionOrder []int
		var mu sync.Mutex

		executors := make([]Executor, 5)
		for i := range 5 {
			taskID := i
			executors[i] = func(ctx context.Context) error {
				current := currentRunning.Add(1)

				// Track max concurrent
				for {
					maxValue := atomic.LoadInt32(&maxConcurrent)
					if current <= maxValue || atomic.CompareAndSwapInt32(&maxConcurrent, maxValue, current) {
						break
					}
				}

				mu.Lock()
				executionOrder = append(executionOrder, taskID)
				mu.Unlock()

				time.Sleep(10 * time.Millisecond)
				currentRunning.Add(-1)
				return nil
			}
		}

		ctx := context.Background()
		err := NewParallelExecutor(1, executors...)(ctx)
		assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

		assert.Equal(t, int32(1), maxConcurrent, "Should never exceed 1 concurrent execution")
		assert.Len(t, executionOrder, 5, "All tasks should execute")
	})

	t.Run("MaxParallel=3 Limited", func(t *testing.T) {
		var currentRunning atomic.Int32
		var maxConcurrent int32

		executors := make([]Executor, 10)
		for i := range 10 {
			executors[i] = func(ctx context.Context) error {
				current := currentRunning.Add(1)

				for {
					maxValue := atomic.LoadInt32(&maxConcurrent)
					if current <= maxValue || atomic.CompareAndSwapInt32(&maxConcurrent, maxValue, current) {
						break
					}
				}

				time.Sleep(20 * time.Millisecond)
				currentRunning.Add(-1)
				return nil
			}
		}

		ctx := context.Background()
		err := NewParallelExecutor(3, executors...)(ctx)
		assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

		assert.LessOrEqual(t, int(maxConcurrent), 3, "Should never exceed 3 concurrent executions")
		assert.GreaterOrEqual(t, int(maxConcurrent), 1, "Should have at least 1 concurrent execution")
	})

	t.Run("MaxParallel=0 Uses1Worker", func(t *testing.T) {
		var maxConcurrent int32
		var currentRunning atomic.Int32

		executors := make([]Executor, 5)
		for i := range 5 {
			executors[i] = func(ctx context.Context) error {
				current := currentRunning.Add(1)

				for {
					maxValue := atomic.LoadInt32(&maxConcurrent)
					if current <= maxValue || atomic.CompareAndSwapInt32(&maxConcurrent, maxValue, current) {
						break
					}
				}

				time.Sleep(10 * time.Millisecond)
				currentRunning.Add(-1)
				return nil
			}
		}

		ctx := context.Background()
		// When maxParallel is 0 or negative, it defaults to 1
		err := NewParallelExecutor(0, executors...)(ctx)
		assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

		assert.Equal(t, int32(1), maxConcurrent, "Should use 1 worker when max-parallel is 0")
	})
}

// TestMaxParallelWithErrors tests error handling with max-parallel
func TestMaxParallelWithErrors(t *testing.T) {
	t.Run("OneTaskFailsOthersContinue", func(t *testing.T) {
		var successCount int32

		executors := make([]Executor, 5)
		for i := range 5 {
			taskID := i
			executors[i] = func(ctx context.Context) error {
				if taskID == 2 {
					return assert.AnError
				}
				atomic.AddInt32(&successCount, 1)
				return nil
			}
		}

		ctx := context.Background()
		err := NewParallelExecutor(2, executors...)(ctx)

		// Should return the error from task 2
		assert.Error(t, err) //nolint:testifylint // pre-existing issue from nektos/act

		// Other tasks should still execute
		assert.Equal(t, int32(4), successCount, "4 tasks should succeed")
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		var startedCount int32
		executors := make([]Executor, 10)
		for i := range 10 {
			executors[i] = func(ctx context.Context) error {
				atomic.AddInt32(&startedCount, 1)
				time.Sleep(100 * time.Millisecond)
				return nil
			}
		}

		// Cancel after a short delay
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()

		err := NewParallelExecutor(3, executors...)(ctx)
		assert.Error(t, err)                     //nolint:testifylint // pre-existing issue from nektos/act
		assert.ErrorIs(t, err, context.Canceled) //nolint:testifylint // pre-existing issue from nektos/act

		// Not all tasks should start due to cancellation (but timing may vary)
		// Just verify cancellation occurred
		t.Logf("Started %d tasks before cancellation", startedCount)
	})
}

// TestMaxParallelResourceSharing tests resource sharing scenarios
func TestMaxParallelResourceSharing(t *testing.T) {
	t.Run("SharedResourceWithMutex", func(t *testing.T) {
		var sharedCounter int
		var mu sync.Mutex

		executors := make([]Executor, 100)
		for i := range 100 {
			executors[i] = func(ctx context.Context) error {
				mu.Lock()
				sharedCounter++
				mu.Unlock()
				return nil
			}
		}

		ctx := context.Background()
		err := NewParallelExecutor(10, executors...)(ctx)
		assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

		assert.Equal(t, 100, sharedCounter, "All tasks should increment counter")
	})

	t.Run("ChannelCommunication", func(t *testing.T) {
		resultChan := make(chan int, 50)

		executors := make([]Executor, 50)
		for i := range 50 {
			taskID := i
			executors[i] = func(ctx context.Context) error {
				resultChan <- taskID
				return nil
			}
		}

		ctx := context.Background()
		err := NewParallelExecutor(5, executors...)(ctx)
		assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

		close(resultChan)

		results := make(map[int]bool)
		for result := range resultChan {
			results[result] = true
		}

		assert.Len(t, results, 50, "All task IDs should be received")
	})
}
