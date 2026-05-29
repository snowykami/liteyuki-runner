// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"testing"

	"gitea.com/gitea/runner/act/model"

	"github.com/stretchr/testify/assert"
	"go.yaml.in/yaml/v4"
)

func TestMaxParallelStrategy(t *testing.T) {
	tests := []struct {
		name                string
		maxParallelString   string
		expectedMaxParallel int
	}{
		{
			name:                "max-parallel-1",
			maxParallelString:   "1",
			expectedMaxParallel: 1,
		},
		{
			name:                "max-parallel-2",
			maxParallelString:   "2",
			expectedMaxParallel: 2,
		},
		{
			name:                "max-parallel-default",
			maxParallelString:   "",
			expectedMaxParallel: 4,
		},
		{
			name:                "max-parallel-10",
			maxParallelString:   "10",
			expectedMaxParallel: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matrix := map[string][]any{
				"version": {1, 2, 3, 4, 5},
			}

			var rawMatrix yaml.Node
			err := rawMatrix.Encode(matrix)
			assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

			job := &model.Job{
				Strategy: &model.Strategy{
					MaxParallelString: tt.maxParallelString,
					RawMatrix:         rawMatrix,
				},
			}

			matrixes, err := job.GetMatrixes()
			assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
			assert.NotNil(t, matrixes)
			assert.Len(t, matrixes, 5)
			assert.Equal(t, tt.expectedMaxParallel, job.Strategy.MaxParallel)
		})
	}
}
