//go:build !windows

package pat

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceFile0600ReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	targetRaw := []byte(`{"target":"unchanged"}`)
	if err := os.WriteFile(target, targetRaw, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(dir, "credential.json")
	if err := os.Symlink(target, destination); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"access_token":"exact-payload"}`)
	if err := replaceFile0600(destination, payload); err != nil {
		t.Fatal(err)
	}

	assertReplacedCredential(t, destination, payload)
	assertFileContent(t, target, targetRaw)
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetInfo.Mode().Perm(); got != 0o640 {
		t.Fatalf("target mode = %o, want 640", got)
	}
}

func TestReplaceFile0600ReplacesRegularFile(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "credential.json")
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := []byte("new credential bytes\n")
	if err := replaceFile0600(destination, payload); err != nil {
		t.Fatal(err)
	}
	assertReplacedCredential(t, destination, payload)
}

func assertReplacedCredential(t *testing.T, path string, expected []byte) {
	t.Helper()
	assertSecuredCredentialFile(t, path)
	assertFileContent(t, path, expected)
}

func assertSecuredCredentialFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("replacement mode = %v, want regular file", info.Mode())
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("replacement mode = %o, want 600", got)
	}
}

func assertFileContent(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("file content = %q, want %q", actual, expected)
	}
}
