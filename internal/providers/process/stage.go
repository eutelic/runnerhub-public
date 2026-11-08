package process

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// stageNativeRunner creates an independent private copy of the trusted
// GitHub runner distribution for each JIT job. os.Root confines every file
// read/write (even when a symlink changes during traversal) to its own root.
// Cached credentials, workspaces and diagnostics are never copied.
func stageNativeRunner(template, destination string) error {
	source, err := filepath.Abs(template)
	if err != nil {
		return err
	}
	dst, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if source == dst || strings.HasPrefix(dst, source+string(os.PathSeparator)) {
		return errors.New("runner worker directory must not be inside the immutable runner template")
	}
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer sourceRoot.Close()
	destRoot, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer destRoot.Close()

	return fs.WalkDir(sourceRoot.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if rel == "." {
			return nil
		}
		if path.Dir(rel) == "." {
			switch entry.Name() {
			case "_work", "_diag", ".runner", ".credentials", ".credentials_rsaparams", ".env", ".path":
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := sourceRoot.Lstat(rel)
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode.IsDir() {
			return destRoot.Mkdir(rel, mode.Perm())
		}
		if mode&os.ModeSymlink != 0 {
			target, err := sourceRoot.Readlink(rel)
			if err != nil {
				return err
			}
			if path.IsAbs(target) {
				return fmt.Errorf("native runner template has absolute symlink %q", rel)
			}
			resolved := path.Clean(path.Join(path.Dir(rel), target))
			if resolved == ".." || strings.HasPrefix(resolved, "../") {
				return fmt.Errorf("native runner template symlink %q escapes template", rel)
			}
			// Root.Symlink ensures the directory entry itself cannot be created
			// outside the worker root. Its relative target was checked above.
			return destRoot.Symlink(target, rel)
		}
		if !mode.IsRegular() {
			return fmt.Errorf("native runner template contains unsupported file %q", rel)
		}
		in, err := sourceRoot.Open(rel)
		if err != nil {
			return err
		}
		out, err := destRoot.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeOutErr := out.Close()
		closeInErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
		return closeInErr
	})
}
