// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePlatform(t *testing.T) {
	t.Run("empty input returns nil platform without error", func(t *testing.T) {
		got, err := parsePlatform("")
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("os/arch", func(t *testing.T) {
		got, err := parsePlatform("linux/amd64")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "linux", got.OS)
		assert.Equal(t, "amd64", got.Architecture)
		assert.Empty(t, got.Variant)
	})

	t.Run("os/arch/variant", func(t *testing.T) {
		got, err := parsePlatform("linux/arm/v7")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "linux", got.OS)
		assert.Equal(t, "arm", got.Architecture)
		assert.Equal(t, "v7", got.Variant)
	})

	t.Run("input is lowercased", func(t *testing.T) {
		got, err := parsePlatform("Linux/AMD64/V8")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "linux", got.OS)
		assert.Equal(t, "amd64", got.Architecture)
		assert.Equal(t, "v8", got.Variant)
	})

	for _, bad := range []string{
		"amd64",
		"linux",
		"linux/",
		"/amd64",
		"/",
		"//",
		"linux/arm/",
		"linux/arm/v7/extra",
	} {
		t.Run("rejects "+bad, func(t *testing.T) {
			got, err := parsePlatform(bad)
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}
