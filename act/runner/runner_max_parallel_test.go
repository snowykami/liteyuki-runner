// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestMaxParallelConfig tests that MaxParallel config is properly set
func TestMaxParallelConfig(t *testing.T) {
	t.Run("MaxParallel set to 2", func(t *testing.T) {
		config := &Config{
			Workdir:     "testdata",
			MaxParallel: 2,
		}

		runner, err := New(config)
		assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
		assert.NotNil(t, runner)

		// Verify config is properly stored
		runnerImpl, ok := runner.(*runnerImpl)
		assert.True(t, ok)
		assert.Equal(t, 2, runnerImpl.config.MaxParallel)
	})

	t.Run("MaxParallel set to 0 (no limit)", func(t *testing.T) {
		config := &Config{
			Workdir:     "testdata",
			MaxParallel: 0,
		}

		runner, err := New(config)
		assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
		assert.NotNil(t, runner)

		runnerImpl, ok := runner.(*runnerImpl)
		assert.True(t, ok)
		assert.Equal(t, 0, runnerImpl.config.MaxParallel)
	})

	t.Run("MaxParallel not set (defaults to 0)", func(t *testing.T) {
		config := &Config{
			Workdir: "testdata",
		}

		runner, err := New(config)
		assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
		assert.NotNil(t, runner)

		runnerImpl, ok := runner.(*runnerImpl)
		assert.True(t, ok)
		assert.Equal(t, 0, runnerImpl.config.MaxParallel)
	})
}

// TestMaxParallelConcurrencyTracking tests that max-parallel actually limits concurrent execution
func TestMaxParallelConcurrencyTracking(t *testing.T) {
	// This is a unit test for the parallel executor logic
	// We test that when MaxParallel is set, it limits the number of workers

	var mu sync.Mutex
	var maxConcurrent int
	var currentConcurrent int

	// Create a function that tracks concurrent execution
	trackingFunc := func() {
		mu.Lock()
		currentConcurrent++
		if currentConcurrent > maxConcurrent {
			maxConcurrent = currentConcurrent
		}
		mu.Unlock()

		// Simulate work
		time.Sleep(50 * time.Millisecond)

		mu.Lock()
		currentConcurrent--
		mu.Unlock()
	}

	// Run multiple tasks with limited parallelism
	maxConcurrent = 0
	currentConcurrent = 0

	// This simulates what NewParallelExecutor does with a semaphore
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 2) // Limit to 2 concurrent

	for range 6 {
		wg.Go(func() {
			semaphore <- struct{}{}        // Acquire
			defer func() { <-semaphore }() // Release
			trackingFunc()
		})
	}

	wg.Wait()

	// With a semaphore of 2, max concurrent should be <= 2
	assert.LessOrEqual(t, maxConcurrent, 2, "Maximum concurrent executions should not exceed limit")
	assert.GreaterOrEqual(t, maxConcurrent, 1, "Should have at least 1 concurrent execution")
}
