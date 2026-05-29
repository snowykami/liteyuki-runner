// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"errors"
	"os"
	"syscall"
)

func getSysProcAttr(cmdLine string, tty bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Rfork: syscall.RFNOTEG,
	}
}

func openPty() (*os.File, *os.File, error) {
	return nil, nil, errors.New("Unsupported")
}
