//go:build linux

package sessionkey

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Workspace retains an exclusive OS lock until TDLib is fully closed. A native
// process must be killed after a shutdown timeout before reusing this directory.
type Workspace struct {
	DatabaseDir string
	FilesDir    string
	DatabaseKey []byte
	lock        *os.File
	once        sync.Once
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil || actual != path {
		return errors.New("session path must not contain symbolic links")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("session directories must have owner-only permissions (0700)")
	}
	return nil
}
func OpenWorkspace(root, accountID string, keyring *Keyring) (*Workspace, error) {
	if !ValidAccountID(accountID) || keyring == nil {
		return nil, errors.New("valid account and keyring are required")
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("session root must be absolute")
	}
	root = filepath.Clean(root)
	if err := privateDir(root); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, accountID)
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(dir, ".lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, errors.New("cannot open session lock")
	}
	f := os.NewFile(uintptr(fd), "session-lock")
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("session directory is already owned by another process")
	}
	w := &Workspace{DatabaseDir: filepath.Join(dir, "db"), FilesDir: filepath.Join(dir, "files"), lock: f}
	ok := false
	defer func() {
		if !ok {
			w.Close()
		}
	}()
	for _, path := range []string{w.DatabaseDir, w.FilesDir} {
		if err := privateDir(path); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(dir, "database-key.enc")
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		w.DatabaseKey = key
		encrypted, err := keyring.Seal(accountID, key)
		if err != nil {
			return nil, err
		}
		tmp, err := os.CreateTemp(dir, ".key-*")
		if err != nil {
			return nil, err
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if _, err := tmp.Write(encrypted); err != nil {
			tmp.Close()
			return nil, err
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return nil, err
		}
		if err := tmp.Close(); err != nil {
			return nil, err
		}
		if err := os.Rename(tmpPath, path); err != nil {
			return nil, err
		}
		d, err := os.Open(dir)
		if err != nil {
			return nil, err
		}
		err = d.Sync()
		d.Close()
		if err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("encrypted session key must be an owner-only regular file")
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 4097))
		file.Close()
		if readErr != nil {
			return nil, readErr
		}
		w.DatabaseKey, err = keyring.Open(accountID, data)
		if err != nil {
			return nil, fmt.Errorf("open TDLib database key: %w", err)
		}
	}
	ok = true
	return w, nil
}
func (w *Workspace) Close() error {
	var result error
	w.once.Do(func() {
		clear(w.DatabaseKey)
		_ = syscall.Flock(int(w.lock.Fd()), syscall.LOCK_UN)
		result = w.lock.Close()
	})
	return result
}
