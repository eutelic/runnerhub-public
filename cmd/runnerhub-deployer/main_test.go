package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPrivateSecretAcceptsPrivateRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPrivateSecret(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "secret-value\n" {
		t.Fatalf("secret=%q", got)
	}
}

func TestReadPrivateSecretRejectsUntrustedPathsAndFiles(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private")
	if err := os.WriteFile(private, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	worldReadable := filepath.Join(dir, "world-readable")
	if err := os.WriteFile(worldReadable, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "linked")
	if err := os.Symlink(private, symlink); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"relative-secret", worldReadable, symlink, dir} {
		t.Run(path, func(t *testing.T) {
			if _, err := readPrivateSecret(path); err == nil {
				t.Fatalf("readPrivateSecret(%q) unexpectedly succeeded", path)
			}
		})
	}
}

func TestValidatePrivateSecretFileInfoRejectsPathSwap(t *testing.T) {
	dir := t.TempDir()
	originalPath := filepath.Join(dir, "original")
	replacementPath := filepath.Join(dir, "replacement")
	for _, path := range []string{originalPath, replacementPath} {
		if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	original, err := os.Lstat(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Stat(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateSecretFileInfo(original, replacement); err == nil {
		t.Fatal("replaced secret file identity was accepted")
	}
}
