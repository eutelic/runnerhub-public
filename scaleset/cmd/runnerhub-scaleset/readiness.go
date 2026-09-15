package main

import (
	"fmt"
	"os"
	"strings"
)

func readinessPath() string {
	return strings.TrimSpace(os.Getenv("RUNNERHUB_READY_FILE"))
}

func setReadiness(ready bool) error {
	path := readinessPath()
	if path == "" {
		return nil
	}
	if ready {
		return writeReadinessFile(path)
	}
	return clearReadinessFile(path)
}

func writeReadinessFile(path string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("ready\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}

func clearReadinessFile(path string) error {
	err := os.Remove(path)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("remove %s: %w", path, err)
}
