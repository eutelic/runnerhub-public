package githubrunner

import "testing"

func TestAPIBasePreservesGHESPrefix(t *testing.T) {
	base, err := parseAPIBase("https://github.example.com/api/v3")
	if err != nil {
		t.Fatal(err)
	}
	got, err := apiEndpoint(base, "/repos/acme/repo/actions/runners/7?x=1")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.example.com/api/v3/repos/acme/repo/actions/runners/7?x=1"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
func TestAPIBaseRejectsRemotePlainHTTP(t *testing.T) {
	if _, err := parseAPIBase("http://github.example.com/api/v3"); err == nil {
		t.Fatal("remote plaintext GitHub API URL accepted")
	}
	if _, err := parseAPIBase("http://127.0.0.1:8080/api/v3"); err != nil {
		t.Fatalf("loopback test URL rejected: %v", err)
	}
}
