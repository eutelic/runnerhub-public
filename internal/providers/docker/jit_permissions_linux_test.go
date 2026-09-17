//go:build linux

package docker

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

	switch got := info.Mode().Perm(); got {
	case 0o400:
		if stat.Uid != runnerUID || stat.Gid != runnerGID {
			t.Fatalf("normalized owner=%d:%d, want %d:%d", stat.Uid, stat.Gid, runnerUID, runnerGID)
		}
	case 0o444:
		// Non-root development environments that cannot chown use the historical
		// read-only fallback. Production agent containers run as root and must take
		// the 0400 + 1001:1001 path above.
	default:
		t.Fatalf("jitconfig mode=%04o, want normalized 0400 or fallback 0444", got)
	}
}
