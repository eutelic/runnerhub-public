package cache

import (
	"bytes"
	"errors"
	"testing"
)

func TestPutOpen(t *testing.T) {
	s := Store{Root: t.TempDir()}
	key, n, err := s.Put(bytes.NewBufferString("hello"))
	if err != nil || n != 5 {
		t.Fatalf("put key=%s n=%d err=%v", key, n, err)
	}
	f, err := s.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}
func TestPutRejectsOversizedObjectInsteadOfTruncating(t *testing.T) {
	s := Store{Root: t.TempDir(), MaxBytes: 4}
	_, n, err := s.Put(bytes.NewBufferString("hello"))
	if !errors.Is(err, ErrTooLarge) || n != 5 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
