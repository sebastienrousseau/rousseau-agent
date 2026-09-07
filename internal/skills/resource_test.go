package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for ResolveResource. Every failure mode from the
// spec's tier-3 safety contract is covered: `..` traversal,
// absolute paths, symlink escapes, directory results, and the
// happy path with a legitimate nested file.

func TestResolveResource_EmptyRelPath(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveResource(dir, "")
	assert.ErrorIs(t, err, ErrResourceEmpty)
}

func TestResolveResource_AbsolutePathRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveResource(dir, "/etc/passwd")
	assert.ErrorIs(t, err, ErrResourceAbsolutePath)
}

func TestResolveResource_EmptyBaseErrors(t *testing.T) {
	_, err := ResolveResource("", "scripts/x.py")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base directory is empty")
}

func TestResolveResource_DotDotTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveResource(dir, "../etc/passwd")
	assert.ErrorIs(t, err, ErrResourceEscapesBase)

	// Multi-level traversal.
	_, err = ResolveResource(dir, "scripts/../../..//etc/passwd")
	assert.ErrorIs(t, err, ErrResourceEscapesBase)
}

func TestResolveResource_HappyPathReturnsAbsolute(t *testing.T) {
	dir := t.TempDir()
	scripts := filepath.Join(dir, "scripts")
	require.NoError(t, os.MkdirAll(scripts, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(scripts, "run.sh"), []byte("#!/bin/sh\n"), 0o600))

	got, err := ResolveResource(dir, "scripts/run.sh")
	require.NoError(t, err)
	// The resolved path is absolute and within the base dir.
	assert.True(t, filepath.IsAbs(got))
	assert.True(t, insideDir(dir, got), "resolved path must be inside base")
}

func TestResolveResource_NonExistentFileStillSafe(t *testing.T) {
	// A skill body might reference a resource that doesn't exist yet
	// (typo, work-in-progress). The safety check runs on the
	// requested path; the caller will get os.ErrNotExist from
	// their own Read.
	dir := t.TempDir()
	got, err := ResolveResource(dir, "scripts/does-not-exist.py")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(got))
	// The file doesn't exist yet, but the safety check passed.
	_, statErr := os.Stat(got)
	assert.True(t, os.IsNotExist(statErr))
}

func TestResolveResource_SymlinkEscapeRejected(t *testing.T) {
	// A skill dir containing a symlink pointing OUTSIDE the base
	// must be caught by EvalSymlinks + insideDir. This is the
	// classic "malicious skill ships a symlink to /etc/passwd"
	// attack.
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.txt")
	require.NoError(t, os.WriteFile(target, []byte("secret"), 0o600))

	linkPath := filepath.Join(dir, "escape")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Skip("cannot create symlinks on this filesystem: " + err.Error())
	}

	_, err := ResolveResource(dir, "escape")
	assert.ErrorIs(t, err, ErrResourceEscapesBase)
}

func TestResolveResource_DirectoryRejected(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "scripts")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	_, err := ResolveResource(dir, "scripts")
	assert.ErrorIs(t, err, ErrResourceIsDir)
}

func TestResolveResource_HandlesRelativeBaseDir(t *testing.T) {
	// baseDir passed as a relative path must be canonicalised
	// before the safety check.
	orig, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(orig) }) //nolint:errcheck // test cleanup, best-effort

	dir := t.TempDir()
	require.NoError(t, os.Chdir(dir))
	require.NoError(t, os.MkdirAll("skill/scripts", 0o755))
	require.NoError(t, os.WriteFile("skill/scripts/x.py", []byte("x"), 0o600))

	got, err := ResolveResource("skill", "scripts/x.py")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(got))
}

// -- insideDir --------------------------------------------------------

func TestInsideDir_ChildIsInside(t *testing.T) {
	assert.True(t, insideDir("/a", "/a"))
	assert.True(t, insideDir("/a", "/a/b"))
	assert.True(t, insideDir("/a", "/a/b/c"))
}

func TestInsideDir_SiblingIsNotInside(t *testing.T) {
	// `/foo` and `/foobar` share a prefix but the second is not a
	// child of the first — the whole point of using filepath.Rel
	// instead of strings.HasPrefix.
	assert.False(t, insideDir("/foo", "/foobar"))
	assert.False(t, insideDir("/foo", "/foobar/x"))
}

func TestInsideDir_ParentIsNotInside(t *testing.T) {
	assert.False(t, insideDir("/a/b", "/a"))
}
