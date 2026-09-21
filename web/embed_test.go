package web

import (
	"regexp"
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
