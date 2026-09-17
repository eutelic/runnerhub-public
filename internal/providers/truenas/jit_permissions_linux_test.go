//go:build linux

package truenas

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteJITSecretNormalizesPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jitconfig")
	if err := writeJITSecret(path, []byte("secret-jit")); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected stat type %T", info.Sys())
	}

	if os.Geteuid() == 0 {
		if got := info.Mode().Perm(); got != 0o400 {
			t.Fatalf("root normalization mode=%04o, want 0400", got)
		}
		if stat.Uid != runnerUID || stat.Gid != runnerGID {
			t.Fatalf("root normalization owner=%d:%d, want %d:%d", stat.Uid, stat.Gid, runnerUID, runnerGID)
		}
		return
	}

	if got := info.Mode().Perm(); got != 0o444 {
		t.Fatalf("non-root fallback mode=%04o, want 0444", got)
	}
}
