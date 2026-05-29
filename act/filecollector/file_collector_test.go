// Copyright 2024 The Gitea Authors. All rights reserved.
// Copyright 2024 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package filecollector

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryFs struct {
	billy.Filesystem
}

func (mfs *memoryFs) walk(root string, fn filepath.WalkFunc) error {
	dir, err := mfs.ReadDir(root)
	if err != nil {
		return err
	}
	for i := range dir {
		filename := filepath.Join(root, dir[i].Name())
		err = fn(filename, dir[i], nil)
		if dir[i].IsDir() {
			if err == filepath.SkipDir {
				err = nil
			} else if err := mfs.walk(filename, fn); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (mfs *memoryFs) Walk(root string, fn filepath.WalkFunc) error {
	stat, err := mfs.Lstat(root)
	if err != nil {
		return err
	}
	err = fn(strings.Join([]string{root, "."}, string(filepath.Separator)), stat, nil)
	if err != nil {
		return err
	}
	return mfs.walk(root, fn)
}

func (mfs *memoryFs) OpenGitIndex(path string) (*index.Index, error) {
	f, _ := mfs.Filesystem.Chroot(filepath.Join(path, ".git")) //nolint:staticcheck // pre-existing issue from nektos/act
	storage := filesystem.NewStorage(f, cache.NewObjectLRUDefault())
	i, err := storage.Index()
	if err != nil {
		return nil, err
	}
	return i, nil
}

func (mfs *memoryFs) Open(path string) (io.ReadCloser, error) {
	return mfs.Filesystem.Open(path)
}

func (mfs *memoryFs) Readlink(path string) (string, error) {
	return mfs.Filesystem.Readlink(path)
}

func TestIgnoredTrackedfile(t *testing.T) {
	fs := memfs.New()
	_ = fs.MkdirAll("mygitrepo/.git", 0o777)
	dotgit, _ := fs.Chroot("mygitrepo/.git")
	worktree, _ := fs.Chroot("mygitrepo")
	repo, _ := git.Init(filesystem.NewStorage(dotgit, cache.NewObjectLRUDefault()), worktree)
	f, _ := worktree.Create(".gitignore")
	_, _ = f.Write([]byte(".*\n"))
	f.Close()
	// This file shouldn't be in the tar
	f, _ = worktree.Create(".env")
	_, _ = f.Write([]byte("test=val1\n"))
	f.Close()
	w, _ := repo.Worktree()
	// .gitignore is in the tar after adding it to the index
	_, _ = w.Add(".gitignore")

	tmpTar, _ := fs.Create("temp.tar")
	tw := tar.NewWriter(tmpTar)
	ps, _ := gitignore.ReadPatterns(worktree, []string{})
	ignorer := gitignore.NewMatcher(ps)
	fc := &FileCollector{
		Fs:        &memoryFs{Filesystem: fs},
		Ignorer:   ignorer,
		SrcPath:   "mygitrepo",
		SrcPrefix: "mygitrepo" + string(filepath.Separator),
		Handler: &TarCollector{
			TarWriter: tw,
		},
	}
	err := fc.Fs.Walk("mygitrepo", fc.CollectFiles(context.Background(), []string{}))
	assert.NoError(t, err, "successfully collect files") //nolint:testifylint // pre-existing issue from nektos/act
	tw.Close()
	_, _ = tmpTar.Seek(0, io.SeekStart)
	tr := tar.NewReader(tmpTar)
	h, err := tr.Next()
	assert.NoError(t, err, "tar must not be empty") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, ".gitignore", h.Name)
	_, err = tr.Next()
	assert.ErrorIs(t, err, io.EOF, "tar must only contain one element")
}

func TestSymlinks(t *testing.T) {
	fs := memfs.New()
	_ = fs.MkdirAll("mygitrepo/.git", 0o777)
	dotgit, _ := fs.Chroot("mygitrepo/.git")
	worktree, _ := fs.Chroot("mygitrepo")
	repo, _ := git.Init(filesystem.NewStorage(dotgit, cache.NewObjectLRUDefault()), worktree)
	// This file shouldn't be in the tar
	f, err := worktree.Create(".env")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	_, err = f.Write([]byte("test=val1\n"))
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	f.Close()
	err = worktree.Symlink(".env", "test.env")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	w, err := repo.Worktree()
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	// .gitignore is in the tar after adding it to the index
	_, err = w.Add(".env")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	_, err = w.Add("test.env")
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	tmpTar, _ := fs.Create("temp.tar")
	tw := tar.NewWriter(tmpTar)
	ps, _ := gitignore.ReadPatterns(worktree, []string{})
	ignorer := gitignore.NewMatcher(ps)
	fc := &FileCollector{
		Fs:        &memoryFs{Filesystem: fs},
		Ignorer:   ignorer,
		SrcPath:   "mygitrepo",
		SrcPrefix: "mygitrepo" + string(filepath.Separator),
		Handler: &TarCollector{
			TarWriter: tw,
		},
	}
	err = fc.Fs.Walk("mygitrepo", fc.CollectFiles(context.Background(), []string{}))
	assert.NoError(t, err, "successfully collect files") //nolint:testifylint // pre-existing issue from nektos/act
	tw.Close()
	_, _ = tmpTar.Seek(0, io.SeekStart)
	tr := tar.NewReader(tmpTar)
	h, err := tr.Next()
	files := map[string]tar.Header{}
	for err == nil {
		files[h.Name] = *h
		h, err = tr.Next()
	}

	assert.Equal(t, ".env", files[".env"].Name)
	assert.Equal(t, "test.env", files["test.env"].Name)
	assert.Equal(t, ".env", files["test.env"].Linkname)
	assert.ErrorIs(t, err, io.EOF, "tar must be read cleanly to EOF")
}

// Regression for https://gitea.com/gitea/runner/issues/876 and /941:
// re-copying an action directory must overwrite a pre-existing read-only
// file (e.g. a git pack .idx at mode 0444) instead of failing with EACCES
// on macOS or "Access is denied" on Windows.
func TestCopyCollectorWriteFileOverwritesReadOnlyFile(t *testing.T) {
	dst := t.TempDir()
	target := filepath.Join(dst, "sub", "pack.idx")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o444))

	src := filepath.Join(t.TempDir(), "pack.idx")
	require.NoError(t, os.WriteFile(src, []byte("new"), 0o444))
	fi, err := os.Stat(src)
	require.NoError(t, err)

	cc := &CopyCollector{DstDir: dst}
	require.NoError(t, cc.WriteFile("sub/pack.idx", fi, "", strings.NewReader("new")))

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))
}

// Without the destination removal, os.Symlink fails with EEXIST when the
// path already holds a regular file from an earlier copy of the action.
func TestCopyCollectorWriteFileOverwritesFileWithSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires elevated privileges on Windows")
	}
	dst := t.TempDir()
	target := filepath.Join(dst, "link")
	require.NoError(t, os.WriteFile(target, []byte("stale"), 0o644))

	fi, err := os.Lstat(target)
	require.NoError(t, err)

	cc := &CopyCollector{DstDir: dst}
	require.NoError(t, cc.WriteFile("link", fi, "target", nil))

	resolved, err := os.Readlink(target)
	require.NoError(t, err)
	assert.Equal(t, "target", resolved)
}
