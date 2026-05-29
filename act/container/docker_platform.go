// Copyright 2026 The Gitea Authors. All rights reserved.
// Copyright 2025 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !(WITHOUT_DOCKER || !(linux || darwin || windows || netbsd))

package container

import (
	"fmt"
	"strings"

	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

// parsePlatform parses an "os/arch[/variant]" string into a Platform. An empty input
// returns (nil, nil), meaning "no platform constraint". A non-empty but malformed
// string is rejected explicitly so it cannot silently fall through to the daemon's
// default architecture.
func parsePlatform(platform string) (*specs.Platform, error) {
	if platform == "" {
		return nil, nil //nolint:nilnil // no platform constraint requested
	}

	parts := strings.Split(platform, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" || (len(parts) == 3 && parts[2] == "") {
		return nil, fmt.Errorf("invalid platform %q: expected os/arch[/variant]", platform)
	}

	spec := &specs.Platform{
		OS:           strings.ToLower(parts[0]),
		Architecture: strings.ToLower(parts[1]),
	}
	if len(parts) == 3 {
		spec.Variant = strings.ToLower(parts[2])
	}

	return spec, nil
}
