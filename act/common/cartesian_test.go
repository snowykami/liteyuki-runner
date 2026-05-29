// Copyright 2026 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCartesianProduct(t *testing.T) {
	assert := assert.New(t)
	input := map[string][]any{
		"foo": {1, 2, 3, 4},
		"bar": {"a", "b", "c"},
		"baz": {false, true},
	}

	output := CartesianProduct(input)
	assert.Len(output, 24)

	for _, v := range output {
		assert.Len(v, 3)

		assert.Contains(v, "foo")
		assert.Contains(v, "bar")
		assert.Contains(v, "baz")
	}

	input = map[string][]any{
		"foo": {1, 2, 3, 4},
		"bar": {},
		"baz": {false, true},
	}
	output = CartesianProduct(input)
	assert.Empty(output)

	input = map[string][]any{}
	output = CartesianProduct(input)
	assert.Empty(output)
}
