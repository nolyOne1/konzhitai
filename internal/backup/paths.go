package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type RunDirectories struct {
	Staging         string
	LocalRepository string
	Restore         string
}

type RunPaths struct{ Root string }

func NewRunPaths(root string) RunPaths {
	if strings.TrimSpace(root) == "" {
		root = defaultBackupRoot
	}
	return RunPaths{Root: filepath.Clean(root)}
}

func (p RunPaths) For(runID string) (RunDirectories, error) {
	parsed, err := uuid.Parse(runID)
	if err != nil || parsed.String() != strings.ToLower(runID) || !filepath.IsAbs(p.Root) {
		return RunDirectories{}, ErrInvalidRequest
	}
	root := filepath.Clean(p.Root)
	if err := ensurePrivateDirectory(root); err != nil {
		return RunDirectories{}, err
	}
	directories := RunDirectories{
		Staging:         filepath.Join(root, "staging", parsed.String()),
		LocalRepository: filepath.Join(root, "local-repo"),
		Restore:         filepath.Join(root, "restore", parsed.String()),
	}
	for _, directory := range []string{directories.Staging, directories.LocalRepository, directories.Restore} {
		if err := ensureInside(root, directory); err != nil {
			return RunDirectories{}, err
		}
		if err := ensurePrivateDirectory(filepath.Dir(directory)); err != nil {
			return RunDirectories{}, err
		}
		if err := ensurePrivateDirectory(directory); err != nil {
			return RunDirectories{}, err
		}
	}
	return directories, nil
}

func ensureInside(root, candidate string) error {
	relative, err := filepath.Rel(root, filepath.Clean(candidate))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("备份目录超出受限根目录")
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("备份路径不是安全目录")
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}
