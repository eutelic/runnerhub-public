package web

import (
	"regexp"
	"strings"
	"testing"
)

func TestDashboardStaticElementReferencesExist(t *testing.T) {
	index, err := content.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := content.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}

	idRE := regexp.MustCompile(`\bid=["']([^"']+)["']`)
	refRE := regexp.MustCompile(`getElementById\(["']([^"']+)["']\)`)
	ids := map[string]bool{}
	for _, match := range idRE.FindAllSubmatch(index, -1) {
		ids[string(match[1])] = true
	}
	for _, match := range refRE.FindAllSubmatch(app, -1) {
		id := string(match[1])
		if !ids[id] {
			t.Errorf("app.js references missing dashboard element id %q", id)
		}
	}
}


func TestDashboardIncludesVercelDeployPreset(t *testing.T) {
	app, err := content.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(app)
	for _, want := range []string{
		"runnerhub-vercel",
		"VERCEL_TOKEN",
		"VERCEL_ORG_ID",
		"VERCEL_PROJECT_ID",
		"const VERCEL_CLI_VERSION='59.16.0'",
		"vercel@'+VERCEL_CLI_VERSION",
		"--prebuilt --prod --skip-domain",
		"promote",
		"container-trusted",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Vercel deploy preset missing %q", want)
		}
	}
}


func TestDashboardIncludesRepositoryVisibilityFilters(t *testing.T) {
	index, err := content.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := content.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	indexText := string(index)
	appText := string(app)
	for _, want := range []string{
		`id="repo-search"`,
		`id="repo-show-public"`,
		"Show public unsupported repositories",
	} {
		if !strings.Contains(indexText, want) {
			t.Fatalf("repository filter UI missing %q", want)
		}
	}
	for _, want := range []string{
		"data-repo-account-toggle",
		"runnerhub-hidden-repo-accounts",
		"runnerhub-show-public-unsupported",
		"rerenderRepositories",
	} {
		if !strings.Contains(appText, want) {
			t.Fatalf("repository filter behavior missing %q", want)
		}
	}
}
