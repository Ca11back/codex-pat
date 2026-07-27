//go:build windows

package pat

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const temporaryFileAttempts = 100

func replaceFile0600(path string, raw []byte) error {
	dir := filepath.Dir(path)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() {
		_ = root.Close()
	}()

	temporary, temporaryName, err := createPrivateTemporaryFile(root)
	if err != nil {
		return err
	}
	defer func() {
		_ = root.Remove(temporaryName)
	}()

	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return root.Rename(temporaryName, filepath.Base(path))
}

func createPrivateTemporaryFile(root *os.Root) (*os.File, string, error) {
	for range temporaryFileAttempts {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", fmt.Errorf("generate temporary file name: %w", err)
		}
		name := fmt.Sprintf(".codex-pat-%x.tmp", random)
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return file, name, err
	}
	return nil, "", fmt.Errorf("create private temporary file: %w", os.ErrExist)
}
