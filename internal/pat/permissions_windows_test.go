//go:build windows

package pat

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReplaceFile0600ReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	targetRaw := []byte(`{"target":"unchanged"}`)
	if err := os.WriteFile(target, targetRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(dir, "credential.json")
	if err := os.Symlink(target, destination); err != nil {
		if errors.Is(err, syscall.ERROR_PRIVILEGE_NOT_HELD) {
			if githubActionsRequiresWindowsSymlinkTest(os.Getenv("GITHUB_ACTIONS")) {
				t.Fatalf("GitHub Actions must enable Windows symlink creation for the credential replacement regression test: %v", err)
			}
			t.Skip("Windows symlink creation requires Developer Mode or SeCreateSymbolicLinkPrivilege")
		}
		t.Fatalf("create Windows symlink regression fixture: %v", err)
	}
	payload := []byte(`{"access_token":"exact-payload"}`)
	if err := replaceFile0600(destination, payload); err != nil {
		t.Fatal(err)
	}

	assertWindowsReplacement(t, destination, payload)
	assertWindowsFileContent(t, target, targetRaw)
}

func TestReplaceFile0600ReplacesRegularFile(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "credential.json")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := []byte("new credential bytes\r\n")
	if err := replaceFile0600(destination, payload); err != nil {
		t.Fatal(err)
	}
	assertWindowsReplacement(t, destination, payload)
}

func TestGitHubActionsRequiresWindowsSymlinkTest(t *testing.T) {
	if !githubActionsRequiresWindowsSymlinkTest("true") {
		t.Fatal("GITHUB_ACTIONS=true must require the Windows symlink regression test")
	}
	for _, value := range []string{"", "false", "TRUE", " true "} {
		if githubActionsRequiresWindowsSymlinkTest(value) {
			t.Fatalf("GITHUB_ACTIONS=%q unexpectedly requires the Windows symlink regression test", value)
		}
	}
}

func githubActionsRequiresWindowsSymlinkTest(value string) bool {
	return value == "true"
}

func assertWindowsReplacement(t *testing.T, path string, expected []byte) {
	t.Helper()
	assertSecuredCredentialFile(t, path)
	assertWindowsFileContent(t, path, expected)
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
}

func assertWindowsFileContent(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("file content = %q, want %q", actual, expected)
	}
}
