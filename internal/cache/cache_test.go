package cache

import (
	"io"
	"strings"
	"testing"
)

func TestCASRoundTrip(t *testing.T) {
	s := Store{Root: t.TempDir()}
	k, n, err := s.Put(strings.NewReader("hello"))
	if err != nil || n != 5 {
		t.Fatal(k, n, err)
	}
	f, err := s.Open(k)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	if string(b) != "hello" {
		t.Fatalf("got %q", b)
	}
}
func TestCASRejectsTraversal(t *testing.T) {
	s := Store{Root: t.TempDir()}
	if _, err := s.Open("../bad"); err == nil {
		t.Fatal("expected invalid key")
	}
}
