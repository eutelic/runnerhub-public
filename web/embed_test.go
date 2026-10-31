package web

import (
	"os/exec"
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

func TestDiagnosticsReadinessScriptIsEmbedded(t *testing.T) {
    page, err := content.ReadFile("diagnostics.html")
    if err != nil { t.Fatal(err) }
    logic, err := content.ReadFile("queue-diagnostics.js")
    if err != nil { t.Fatal(err) }
    glue, err := content.ReadFile("diagnostics.js")
    if err != nil { t.Fatal(err) }
    if !strings.Contains(string(page), "id=\"queue-readiness\"") {
        t.Fatal("runner readiness section missing")
    }
    if !strings.Contains(string(page), "src=\"/queue-diagnostics.js\"") {
        t.Fatal("runner readiness script not loaded")
    }
    if !strings.Contains(string(logic), "function summarize(") ||
        !strings.Contains(string(glue), "showReadiness(pools,scalesets,runtime)") {
        t.Fatal("queue readiness analysis not wired to authenticated endpoints")
    }
}

func TestQueueReadinessJavaScript(t *testing.T) {
    node, err := exec.LookPath("node")
    if err != nil {
        t.Skip("node is unavailable in this runner; browser integration is covered by embed checks")
    }
    command := exec.Command(node, "--test", "queue-diagnostics.test.cjs")
    output, err := command.CombinedOutput()
    if err != nil {
        t.Fatalf("Queue diagnostic JavaScript checks failed: %s: %v", string(output), err)
    }
}

func TestDashboardUXJavaScript(t *testing.T) {
    node, err := exec.LookPath("node")
    if err != nil {
        t.Skip("node is unavailable; dashboard UX checks run where Node is installed")
    }
    command := exec.Command(node, "--test", "dashboard-ux.test.cjs")
    output, err := command.CombinedOutput()
    if err != nil {
        t.Fatalf("Dashboard UX JavaScript checks failed: %s: %v", string(output), err)
    }
}

func TestIncidentWorkbenchJavaScript(t *testing.T) {
    node, err := exec.LookPath("node")
    if err != nil { t.Skip("node is unavailable") }
    command := exec.Command(node, "--test", "incident-workbench.test.cjs")
    output, err := command.CombinedOutput()
    if err != nil { t.Fatalf("incident workbench tests failed: %s: %v", string(output), err) }
}
