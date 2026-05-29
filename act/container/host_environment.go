// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/filecollector"
	"gitea.com/gitea/runner/act/lookpath"

	"github.com/go-git/go-billy/v5/helper/polyfill"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"golang.org/x/term"
)

type HostEnvironment struct {
	Path      string
	TmpDir    string
	ToolCache string
	Workdir   string
	// CleanWorkdir means teardown owns Workdir and may delete it. Leave false
	// when Workdir points at a caller-owned checkout (e.g. `act` local mode).
	CleanWorkdir bool
	ActPath      string
	CleanUp      func()
	StdOut       io.Writer
	AllocatePTY  bool // allocate a pseudo-TTY for each step's process
}

func (e *HostEnvironment) Create(_, _ []string) common.Executor {
	return func(ctx context.Context) error {
		return nil
	}
}

func (e *HostEnvironment) ConnectToNetwork(name string) common.Executor {
	return func(ctx context.Context) error {
		return nil
	}
}

func (e *HostEnvironment) Close() common.Executor {
	return func(ctx context.Context) error {
		return nil
	}
}

func (e *HostEnvironment) Copy(destPath string, files ...*FileEntry) common.Executor {
	return func(ctx context.Context) error {
		for _, f := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(destPath, f.Name)), 0o777); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(destPath, f.Name), []byte(f.Body), fs.FileMode(f.Mode)); err != nil {
				return err
			}
		}
		return nil
	}
}

func (e *HostEnvironment) CopyTarStream(ctx context.Context, destPath string, tarStream io.Reader) error {
	if err := os.RemoveAll(destPath); err != nil {
		return err
	}
	tr := tar.NewReader(tarStream)
	cp := &filecollector.CopyCollector{
		DstDir: destPath,
	}
	for {
		ti, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if ti.FileInfo().IsDir() {
			continue
		}
		if ctx.Err() != nil {
			return errors.New("CopyTarStream has been cancelled")
		}
		if err := cp.WriteFile(ti.Name, ti.FileInfo(), ti.Linkname, tr); err != nil {
			return err
		}
	}
}

func (e *HostEnvironment) CopyDir(destPath, srcPath string, useGitIgnore bool) common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		srcPrefix := filepath.Dir(srcPath)
		if !strings.HasSuffix(srcPrefix, string(filepath.Separator)) {
			srcPrefix += string(filepath.Separator)
		}
		logger.Debugf("Stripping prefix:%s src:%s", srcPrefix, srcPath)
		var ignorer gitignore.Matcher
		if useGitIgnore {
			ps, err := gitignore.ReadPatterns(polyfill.New(osfs.New(srcPath)), nil)
			if err != nil {
				logger.Debugf("Error loading .gitignore: %v", err)
			}

			ignorer = gitignore.NewMatcher(ps)
		}
		fc := &filecollector.FileCollector{
			Fs:        &filecollector.DefaultFs{},
			Ignorer:   ignorer,
			SrcPath:   srcPath,
			SrcPrefix: srcPrefix,
			Handler: &filecollector.CopyCollector{
				DstDir: destPath,
			},
		}
		return filepath.Walk(srcPath, fc.CollectFiles(ctx, []string{}))
	}
}

func (e *HostEnvironment) GetContainerArchive(ctx context.Context, srcPath string) (io.ReadCloser, error) {
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	defer tw.Close()
	srcPath = filepath.Clean(srcPath)
	fi, err := os.Lstat(srcPath)
	if err != nil {
		return nil, err
	}
	tc := &filecollector.TarCollector{
		TarWriter: tw,
	}
	if fi.IsDir() {
		srcPrefix := srcPath
		if !strings.HasSuffix(srcPrefix, string(filepath.Separator)) {
			srcPrefix += string(filepath.Separator)
		}
		fc := &filecollector.FileCollector{
			Fs:        &filecollector.DefaultFs{},
			SrcPath:   srcPath,
			SrcPrefix: srcPrefix,
			Handler:   tc,
		}
		err = filepath.Walk(srcPath, fc.CollectFiles(ctx, []string{}))
		if err != nil {
			return nil, err
		}
	} else {
		var f io.ReadCloser
		var linkname string
		if fi.Mode()&fs.ModeSymlink != 0 {
			linkname, err = os.Readlink(srcPath)
			if err != nil {
				return nil, err
			}
		} else {
			f, err = os.Open(srcPath)
			if err != nil {
				return nil, err
			}
			defer f.Close()
		}
		err := tc.WriteFile(fi.Name(), fi, linkname, f)
		if err != nil {
			return nil, err
		}
	}
	return io.NopCloser(buf), nil
}

func (e *HostEnvironment) Pull(_ bool) common.Executor {
	return func(ctx context.Context) error {
		return nil
	}
}

func (e *HostEnvironment) Start(_ bool) common.Executor {
	return func(ctx context.Context) error {
		return nil
	}
}

type ptyWriter struct {
	Out       io.Writer
	AutoStop  atomic.Bool
	dirtyLine bool
}

func (w *ptyWriter) Write(buf []byte) (int, error) {
	if w.AutoStop.Load() && len(buf) > 0 && buf[len(buf)-1] == 4 {
		n, err := w.Out.Write(buf[:len(buf)-1])
		if err != nil {
			return n, err
		}
		if w.dirtyLine || len(buf) > 1 && buf[len(buf)-2] != '\n' {
			_, _ = w.Out.Write([]byte("\n"))
			return n, io.EOF
		}
		return n, io.EOF
	}
	w.dirtyLine = strings.LastIndex(string(buf), "\n") < len(buf)-1
	return w.Out.Write(buf)
}

type localEnv struct {
	env map[string]string
}

func (l *localEnv) Getenv(name string) string {
	if runtime.GOOS == "windows" {
		for k, v := range l.env {
			if strings.EqualFold(name, k) {
				return v
			}
		}
		return ""
	}
	return l.env[name]
}

func lookupPathHost(cmd string, env map[string]string, writer io.Writer) (string, error) {
	f, err := lookpath.LookPath2(cmd, &localEnv{env: env})
	if err != nil {
		err := "Cannot find: " + cmd + " in PATH"
		if _, _err := writer.Write([]byte(err + "\n")); _err != nil {
			return "", fmt.Errorf("%v: %w", err, _err)
		}
		return "", errors.New(err)
	}
	return f, nil
}

func setupPty(cmd *exec.Cmd, cmdline string) (*os.File, *os.File, error) {
	ppty, tty, err := openPty()
	if err != nil {
		return nil, nil, err
	}
	if term.IsTerminal(int(tty.Fd())) {
		_, err := term.MakeRaw(int(tty.Fd()))
		if err != nil {
			ppty.Close()
			tty.Close()
			return nil, nil, err
		}
	}
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = getSysProcAttr(cmdline, true)
	return ppty, tty, nil
}

func writeKeepAlive(ppty io.Writer) {
	c := 1
	var err error
	for c == 1 && err == nil {
		c, err = ppty.Write([]byte{4})
		<-time.After(time.Second)
	}
}

func copyPtyOutput(writer io.Writer, ppty io.Reader, finishLog context.CancelFunc) {
	defer func() {
		finishLog()
	}()
	if _, err := io.Copy(writer, ppty); err != nil {
		return
	}
}

func (e *HostEnvironment) UpdateFromImageEnv(_ *map[string]string) common.Executor {
	return func(ctx context.Context) error {
		return nil
	}
}

func getEnvListFromMap(env map[string]string) []string {
	envList := make([]string, 0)
	for k, v := range env {
		envList = append(envList, fmt.Sprintf("%s=%s", k, v))
	}
	return envList
}

func (e *HostEnvironment) exec(ctx context.Context, command []string, cmdline string, env map[string]string, _, workdir string) error {
	envList := getEnvListFromMap(env)
	var wd string
	if workdir != "" {
		if filepath.IsAbs(workdir) {
			wd = workdir
		} else {
			wd = filepath.Join(e.Path, workdir)
		}
	} else {
		wd = e.Path
	}
	f, err := lookupPathHost(command[0], env, e.StdOut)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, f)
	cmd.Path = f
	cmd.Args = command
	cmd.Stdin = nil
	cmd.Stdout = e.StdOut
	cmd.Env = envList
	cmd.Stderr = e.StdOut
	cmd.Dir = wd
	cmd.SysProcAttr = getSysProcAttr(cmdline, false)
	var ppty *os.File
	var tty *os.File
	defer func() {
		if ppty != nil {
			ppty.Close()
		}
		if tty != nil {
			tty.Close()
		}
	}()
	if e.AllocatePTY {
		var err error
		ppty, tty, err = setupPty(cmd, cmdline)
		if err != nil {
			common.Logger(ctx).Debugf("Failed to setup Pty %v\n", err.Error())
		}
	}
	var writer *ptyWriter
	var logctx context.Context
	if ppty != nil {
		writer = &ptyWriter{Out: e.StdOut}
		var finishLog context.CancelFunc
		logctx, finishLog = context.WithCancel(context.Background())
		go copyPtyOutput(writer, ppty, finishLog)
		go writeKeepAlive(ppty)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	err = cmd.Wait()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ExitCodeError(exitErr.ExitCode())
		}
		return err
	}
	if tty != nil {
		writer.AutoStop.Store(true)
		if _, err := tty.WriteString("\x04"); err != nil {
			common.Logger(ctx).Debug("Failed to write EOT")
		}
		<-logctx.Done()
		ppty.Close()
		ppty = nil
	}
	return err
}

func (e *HostEnvironment) Exec(command []string /*cmdline string, */, env map[string]string, user, workdir string) common.Executor {
	return e.ExecWithCmdLine(command, "", env, user, workdir)
}

func (e *HostEnvironment) ExecWithCmdLine(command []string, cmdline string, env map[string]string, user, workdir string) common.Executor {
	return func(ctx context.Context) error {
		if err := e.exec(ctx, command, cmdline, env, user, workdir); err != nil {
			select {
			case <-ctx.Done():
				return fmt.Errorf("this step has been cancelled: %w", err)
			default:
				return err
			}
		}
		return nil
	}
}

func (e *HostEnvironment) UpdateFromEnv(srcPath string, env *map[string]string) common.Executor {
	return parseEnvFile(e, srcPath, env)
}

func removePathWithRetry(ctx context.Context, path string) error {
	if path == "" {
		return nil
	}
	attempts := 1
	delay := time.Duration(0)
	if runtime.GOOS == "windows" {
		attempts = 5
		delay = 200 * time.Millisecond
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		lastErr = os.RemoveAll(path)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// buildWindowsWorkspaceKillScript builds a PowerShell command that `taskkill
// /T /F`s every process tree whose ExecutablePath or CommandLine references one
// of the given absolute workspace dirs, releasing file handles for cleanup.
//
// Win32_Process is used because it exposes both ExecutablePath and CommandLine
// (Get-Process doesn't, wmic is deprecated). Both match the dir+separator
// prefix, so a sibling dir sharing a name prefix (job1 vs job10) is spared.
// Ordinal String methods, not -like, so path metacharacters ([ ] ? *) stay
// literal.
//
// Pure function so the quote-escaping can be unit-tested without PowerShell.
func buildWindowsWorkspaceKillScript(dirs []string) string {
	quoted := make([]string, len(dirs))
	for i, d := range dirs {
		// Single-quoted PowerShell literal; escape ' by doubling it.
		quoted[i] = "'" + strings.ReplaceAll(d, "'", "''") + "'"
	}

	return `$paths = @(` + strings.Join(quoted, ",") + `)
$selfPid = $PID
Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
    if ($_.ProcessId -eq $selfPid) { return $false }
    foreach ($p in $paths) {
        $prefix = $p + '\'
        if ($_.ExecutablePath -and $_.ExecutablePath.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) { return $true }
        if ($_.CommandLine -and $_.CommandLine.IndexOf($prefix, [System.StringComparison]::OrdinalIgnoreCase) -ge 0) { return $true }
    }
    return $false
} | ForEach-Object {
    & taskkill.exe /PID $_.ProcessId /T /F 2>$null | Out-Null
}
`
}

func (e *HostEnvironment) terminateRunningProcesses(ctx context.Context) {
	if runtime.GOOS != "windows" {
		return
	}

	// Detached: exec.CommandContext won't start on a cancelled ctx, and a
	// server cancel has already cancelled the parent ctx.
	killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer killCancel()

	logger := common.Logger(ctx)

	// Workspace dirs we own. Any process running from or referencing one is a
	// leftover job process. ToolCache is shared across jobs; Workdir only when
	// we own it (else it's a caller-provided checkout, e.g. act local mode).
	owned := []string{e.Path, e.TmpDir}
	if e.CleanWorkdir {
		owned = append(owned, e.Workdir)
	}
	dirs := make([]string, 0, len(owned))
	for _, d := range owned {
		if d == "" {
			continue
		}
		abs, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		dirs = append(dirs, abs)
	}
	if len(dirs) == 0 {
		return
	}

	script := buildWindowsWorkspaceKillScript(dirs)

	cmd := exec.CommandContext(killCtx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.Debugf("workspace process-tree kill via PowerShell failed: %v output=%s", err, strings.TrimSpace(string(out)))
	}
}

func (e *HostEnvironment) Remove() common.Executor {
	return func(ctx context.Context) error {
		// Ensure any lingering child processes are ended before attempting
		// to remove the workspace (Windows file locks otherwise prevent cleanup).
		e.terminateRunningProcesses(ctx)

		// Only removes per-job misc state. Must not remove the cache/toolcache root.
		if e.CleanUp != nil {
			e.CleanUp()
		}

		// Detach: a cancelled ctx would skip removePathWithRetry's retries,
		// which absorb Windows file-handle release lag after the kill above.
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rmCancel()

		logger := common.Logger(ctx)
		var errs []error
		if err := removePathWithRetry(rmCtx, e.Path); err != nil {
			logger.Warnf("failed to remove host misc state %s: %v", e.Path, err)
			errs = append(errs, err)
		}
		if e.CleanWorkdir {
			if err := removePathWithRetry(rmCtx, e.Workdir); err != nil {
				logger.Warnf("failed to remove host workspace %s: %v", e.Workdir, err)
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

func (e *HostEnvironment) ToContainerPath(path string) string {
	if bp, err := filepath.Rel(e.Workdir, path); err != nil {
		return filepath.Join(e.Path, bp)
	} else if filepath.Clean(e.Workdir) == filepath.Clean(path) {
		return e.Path
	}
	return path
}

func (e *HostEnvironment) GetActPath() string {
	actPath := e.ActPath
	if runtime.GOOS == "windows" {
		actPath = strings.ReplaceAll(actPath, "\\", "/")
	}
	return actPath
}

func (*HostEnvironment) GetPathVariableName() string {
	switch runtime.GOOS {
	case "plan9":
		return "path"
	case "windows":
		return "Path" // Actually we need a case insensitive map
	}
	return "PATH"
}

func (e *HostEnvironment) DefaultPathVariable() string {
	v, _ := os.LookupEnv(e.GetPathVariableName())
	return v
}

func (*HostEnvironment) JoinPathVariable(paths ...string) string {
	return strings.Join(paths, string(filepath.ListSeparator))
}

// Reference for Arch values for runner.arch
// https://docs.github.com/en/actions/learn-github-actions/contexts#runner-context
func goArchToActionArch(arch string) string {
	archMapper := map[string]string{
		"x86_64":  "X64",
		"386":     "X86",
		"aarch64": "ARM64",
	}
	if arch, ok := archMapper[arch]; ok {
		return arch
	}
	return arch
}

func goOsToActionOs(os string) string {
	osMapper := map[string]string{
		"darwin": "macOS",
	}
	if os, ok := osMapper[os]; ok {
		return os
	}
	return os
}

func (e *HostEnvironment) GetRunnerContext(_ context.Context) map[string]any {
	return map[string]any{
		"os":         goOsToActionOs(runtime.GOOS),
		"arch":       goArchToActionArch(runtime.GOARCH),
		"temp":       e.TmpDir,
		"tool_cache": e.ToolCache,
	}
}

func (e *HostEnvironment) ReplaceLogWriter(stdout, _ io.Writer) (io.Writer, io.Writer) {
	org := e.StdOut
	e.StdOut = stdout
	return org, org
}

func (*HostEnvironment) IsEnvironmentCaseInsensitive() bool {
	return runtime.GOOS == "windows"
}
