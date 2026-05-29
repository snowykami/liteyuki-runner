// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build (!windows && !plan9 && !openbsd) || (!windows && !plan9 && !mips64)

package container

import (
	"os"
	"syscall"

	"github.com/creack/pty"
)

func getSysProcAttr(_ string, tty bool) *syscall.SysProcAttr {
	if tty {
		return &syscall.SysProcAttr{
			Setsid:  true,
			Setctty: true,
		}
	}
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}

func openPty() (*os.File, *os.File, error) {
	return pty.Open()
}
