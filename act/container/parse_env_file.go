// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"archive/tar"
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"gitea.com/gitea/runner/act/common"
)

func parseEnvFile(e Container, srcPath string, env *map[string]string) common.Executor {
	localEnv := *env
	return func(ctx context.Context) error {
		envTar, err := e.GetContainerArchive(ctx, srcPath)
		if err != nil {
			return nil
		}
		defer envTar.Close()
		reader := tar.NewReader(envTar)
		_, err = reader.Next()
		if err != nil && err != io.EOF {
			return err
		}
		s := bufio.NewScanner(reader)
		// Default 64 KiB max token size is too small for realistic env-file lines; allow up to 16 MiB.
		s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for s.Scan() {
			line := s.Text()
			singleLineEnv := strings.Index(line, "=")
			multiLineEnv := strings.Index(line, "<<")
			if singleLineEnv != -1 && (multiLineEnv == -1 || singleLineEnv < multiLineEnv) {
				localEnv[line[:singleLineEnv]] = line[singleLineEnv+1:]
			} else if multiLineEnv != -1 {
				multiLineEnvContent := ""
				multiLineEnvDelimiter := line[multiLineEnv+2:]
				delimiterFound := false
				for s.Scan() {
					content := s.Text()
					if content == multiLineEnvDelimiter {
						delimiterFound = true
						break
					}
					if multiLineEnvContent != "" {
						multiLineEnvContent += "\n"
					}
					multiLineEnvContent += content
				}
				if err := s.Err(); err != nil {
					return fmt.Errorf("reading env file: %w", err)
				}
				if !delimiterFound {
					return fmt.Errorf("invalid format delimiter '%v' not found before end of file", multiLineEnvDelimiter)
				}
				localEnv[line[:multiLineEnv]] = multiLineEnvContent
			} else {
				return fmt.Errorf("invalid format '%v', expected a line with '=' or '<<'", line)
			}
		}
		if err := s.Err(); err != nil {
			return fmt.Errorf("reading env file: %w", err)
		}
		env = &localEnv
		return nil
	}
}
