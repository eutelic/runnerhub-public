package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
)

const baselineDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const updatedDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const otherDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

type fakeRegistryDigest struct {
	seen map[string]int
	missingSHA bool
	unchanged bool
	mismatch bool
	mutateAfterCheck bool
	nextDigest string
}

func (f *fakeRegistryDigest) Digest(_ context.Context, image string) (string, error) {
	if f.seen == nil { f.seen = make(map[string]int) }
	f.seen[image]++
	if f.nextDigest != "" { return f.nextDigest, nil }
	if strings.HasSuffix(image, ":latest") {
		if f.seen[image] == 1 { return baselineDigest, nil }
		if f.unchanged { return baselineDigest, nil }
		if f.mutateAfterCheck && f.seen[image] >= 3 { return otherDigest, nil }
		return updatedDigest, nil
	}
	if strings.Contains(image, ":sha-") {
		if f.missingSHA { return "", errors.New("manifest not found") }
		if f.mismatch { return otherDigest, nil }
		return updatedDigest, nil
	}
	return "", errors.New("unexpected image reference")
}

type fakePublisher struct {
	meta githubrunner.WorkflowRunMetadata
	found bool
	err error
}

func (f *fakePublisher) LatestPublicationRun(_ context.Context, scope core.GitHubScope, workflow, branch string) (githubrunner.WorkflowRunMetadata, bool, error) {
	if scope.Owner != "eutelic" || scope.Repository != "file-ingest" || workflow != "publish-images.yml" || branch != "main" {
		return githubrunner.WorkflowRunMetadata{}, false, nil
	}
	return f.meta, f.found, f.err
}

func autoTestRun(id int64, conclusion string) githubrunner.WorkflowRunMetadata {
	return githubrunner.WorkflowRunMetadata{
		ID: id, Attempt: 1, Status: "completed", Conclusion: conclusion,
		Event: "push", HeadBranch: "main",
		HeadSHA: "0123456789abcdef0123456789abcdef01234567",
		Path: ".github/workflows/publish-images.yml@main",
	}
}

func newAutoTestManager(t *testing.T) (*BootstrapManager, *fakeManagedTrueNAS, *Service) {
	t.Helper()
	root := t.TempDir()
	fake := testManagedInventory()
	for i := range fake.inventory {
		if fake.inventory[i].Name == "file-ingest" {
            fake.inventory[i].Images = []string{"ghcr.io/eutelic/file-ingest:latest"}
            fake.inventory[i].CustomApp = true
        }
	}
	svc := newManagedService(t, fake, t.TempDir())
	if err := svc.setManaged("file-ingest", true); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(svc.Handler())
	t.Cleanup(server.Close)
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secrets", "truenas-api-key"), svc.ManagementKey, 0600); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(bootstrapState{DeployerURL: server.URL})
	if err := os.WriteFile(filepath.Join(root, "state.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	return &BootstrapManager{Root: root, HTTP: server.Client(), RegistryDigests: &fakeRegistryDigest{}}, fake, svc
}

func TestAutoPublishRedeploysOnlyNewVerifiedSuccess(t *testing.T) {
	manager, fake, _ := newAutoTestManager(t)
	rule := AutoPublishRule{App: "file-ingest", Repository: "eutelic/file-ingest", Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest", Branch: "main", LastRunID: 10, LastAttempt: 1}
	if err := manager.SetAutoPublish(rule); err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{meta: autoTestRun(10, "success"), found: true}
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 0 {
		t.Fatalf("historical success redeployed app: %v", fake.apps)
	}
	publisher.meta = autoTestRun(11, "success")
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 1 || fake.apps[0] != "file-ingest" {
		t.Fatalf("redeploy count=%d apps=%v", len(fake.apps), fake.apps)
	}
	restarted := &BootstrapManager{Root: manager.Root, HTTP: manager.HTTP, RegistryDigests: manager.RegistryDigests}
	if err := restarted.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 1 {
		t.Fatal("same publication replayed after restart")
	}
	rules, err := restarted.AutoPublishRules()
	if err != nil || len(rules) != 1 || rules[0].LastRunID != 11 || rules[0].LastResult != "deployed" {
		t.Fatalf("cursor not persisted: %+v %v", rules, err)
	}
	if err := restarted.DisableAutoPublish(rule.App); err != nil {
		t.Fatal(err)
	}
	publisher.meta = autoTestRun(12, "success")
	if err := restarted.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 1 {
		t.Fatal("auto publisher ran after disabling rule")
	}
}

func TestAutoPublishSkipsUnverifiedAndDisabledApp(t *testing.T) {
	manager, fake, service := newAutoTestManager(t)
	rule := AutoPublishRule{App: "file-ingest", Repository: "eutelic/file-ingest", Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest", Branch: "main"}
	if err := manager.SetAutoPublish(rule); err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{meta: autoTestRun(20, "failure"), found: true}
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 0 {
		t.Fatal("failed publication triggered app redeploy")
	}
	rules, err := manager.AutoPublishRules()
	if err != nil || rules[0].LastResult != "skipped-unverified" {
		t.Fatalf("unverified run should be recorded: %+v %v", rules, err)
	}
	publisher.meta = autoTestRun(21, "success")
	publisher.meta.Event = "pull_request"
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 0 {
		t.Fatal("PR workflow triggered app redeploy")
	}
	if err := service.setManaged("file-ingest", false); err != nil {
		t.Fatal(err)
	}
	publisher.meta = autoTestRun(22, "success")
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 0 {
		t.Fatal("app disabled in standalone deployer but auto redeployed")
	}
}

func TestAutoPublishDoesNotRedeployAfterTrueNASImageChange(t *testing.T) {
	manager, fake, _ := newAutoTestManager(t)
	rule := AutoPublishRule{
		App: "file-ingest", Repository: "eutelic/file-ingest",
		Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest", Branch: "main",
	}
	if err := manager.SetAutoPublish(rule); err != nil { t.Fatal(err) }
	for i := range fake.inventory {
		if fake.inventory[i].Name == "file-ingest" { fake.inventory[i].Images = []string{"ghcr.io/eutelic/unrelated:latest"} }
	}
	publisher := &fakePublisher{meta: autoTestRun(30, "success"), found: true}
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil { t.Fatal(err) }
	if len(fake.apps) > 0 { t.Fatalf("unrelated image was redeployed: %v", fake.apps) }
	rules, err := manager.AutoPublishRules()
	if err != nil || rules[0].LastResult != "skipped-image-mismatch" {
		t.Fatalf("want image mismatch status: %+v %v", rules, err)
	}
}

func TestAutoPublisherValidation(t *testing.T) {
	for _, tc := range []AutoPublishRule{
		{App: "runnerhub", Repository: "eutelic/foo", Workflow: "publish-images.yml", Branch: "develop"},
		{App: "file-ingest", Repository: "../../other", Workflow: "publish-images.yml", Branch: "main"},
		{App: "file-ingest", Repository: "eutelic/file-ingest", Workflow: "../publish.yml", Branch: "main"},
	} {
		if err := validateAutoRule(tc); err == nil {
			t.Fatalf("unsafe auto-deployment rule accepted: %+v", tc)
		}
	}
}

func TestAutoPublishResultUpdatesLaterSortedApp(t *testing.T) {
	manager, _, _ := newAutoTestManager(t)
	first := AutoPublishRule{
		App: "alpha-app", Repository: "eutelic/alpha-app",
		Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/alpha-app:latest",
		Branch: "main", LastRunID: 5, LastAttempt: 1,
	}
	later := AutoPublishRule{
		App: "file-ingest", Repository: "eutelic/file-ingest",
		Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest",
		Branch: "main", LastRunID: 10, LastAttempt: 1,
	}
	for _, rule := range []AutoPublishRule{later, first} {
		if err := manager.SetAutoPublish(rule); err != nil {
			t.Fatal(err)
		}
	}
	check := func(wantLater string) {
		t.Helper()
		rules, err := manager.AutoPublishRules()
		if err != nil {
			t.Fatal(err)
		}
		if len(rules) != 2 || rules[0].App != "alpha-app" || rules[1].App != "file-ingest" {
			t.Fatalf("rules must be sorted by app: %+v", rules)
		}
		if rules[0].LastRunID != 5 || rules[0].LastResult != "watching" || rules[0].LastDetail != "" {
			t.Fatalf("unrelated first app was changed: %+v", rules[0])
		}
		if rules[1].LastResult != wantLater {
			t.Fatalf("later app status=%q, want %q: %+v", rules[1].LastResult, wantLater, rules[1])
		}
	}
	for _, tc := range []struct {
		runID int64
		result string
		cause error
	}{
		{runID: 11, result: "deployed"},
		{runID: 12, result: "failed", cause: errors.New("simulated redeploy failure")},
	} {
		meta := autoTestRun(tc.runID, "success")
		claimed, err := manager.claimPublication(later, meta, "deploying")
		if err != nil || !claimed {
			t.Fatalf("claim later app run %d: claimed=%v err=%v", tc.runID, claimed, err)
		}
		if err := manager.publicationResult(later, meta, tc.result, tc.cause); err != nil {
			t.Fatalf("record later app run %d: %v", tc.runID, err)
		}
		check(tc.result)
		rules, err := manager.AutoPublishRules()
		if err != nil {
			t.Fatal(err)
		}
		if rules[1].LastRunID != tc.runID || rules[1].LastAttempt != 1 {
			t.Fatalf("incorrect publication cursor for later app: %+v", rules[1])
		}
		if tc.cause == nil && rules[1].LastDetail != "" ||
			tc.cause != nil && rules[1].LastDetail == "" {
			t.Fatalf("incorrect success/failure detail: %+v", rules[1])
		}
	}
}


func TestAutoPublishInFlightRunCompletesOnce(t *testing.T) {
	manager, fake, _ := newAutoTestManager(t)
	// The control-plane API intentionally uses a zero cursor when the newest
	// publishing workflow has not completed at the time the checkbox is enabled.
	rule := AutoPublishRule{
		App: "file-ingest", Repository: "eutelic/file-ingest",
		Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest",
		Branch: "main",
	}
	if err := manager.SetAutoPublish(rule); err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{meta: autoTestRun(314, ""), found: true}
	publisher.meta.Status = "in_progress"
	for i := 0; i < 2; i++ {
		if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.apps) != 0 {
		t.Fatalf("in-progress publishing triggered premature redeploy: %v", fake.apps)
	}
	publisher.meta.Status = "completed"
	publisher.meta.Conclusion = "success"
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	restarted := &BootstrapManager{Root: manager.Root, HTTP: manager.HTTP, RegistryDigests: manager.RegistryDigests}
	if err := restarted.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 1 {
		t.Fatalf("expected exactly one redeploy after publishing completed, got %v", fake.apps)
	}
	rules, err := restarted.AutoPublishRules()
	if err != nil || len(rules) != 1 || rules[0].LastRunID != 314 ||
		rules[0].LastResult != "deployed" {
		t.Fatalf("invalid persistent publishing cursor: %+v, err=%v", rules, err)
	}
}


func TestAutoPublishInterruptedClaimRequiresManualRecovery(t *testing.T) {
	manager, fake, _ := newAutoTestManager(t)
	rule := AutoPublishRule{
		App: "file-ingest", Repository: "eutelic/file-ingest",
		Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest",
		Branch: "main", LastRunID: 10, LastAttempt: 1,
	}
	if err := manager.SetAutoPublish(rule); err != nil {
		t.Fatal(err)
	}
	if claimed, err := manager.claimPublication(rule, autoTestRun(11, "success"), "deploying"); err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	publisher := &fakePublisher{meta: autoTestRun(12, "success"), found: true}
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 0 {
		t.Fatalf("newer publication ran during an active claim: %v", fake.apps)
	}
	manager.autoMu.Lock()
	file, err := manager.readAutoRulesLocked()
	if err == nil {
		file.Rules[0].UpdatedAt = time.Now().Add(-16 * time.Minute)
		err = manager.saveAutoRulesLocked(file)
	}
	manager.autoMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a control-plane restart after the TrueNAS call has an
	// unknown outcome. An unacknowledged earlier release must block newer
	// releases instead of silently advancing or issuing a duplicate call.
	restarted := &BootstrapManager{Root: manager.Root, HTTP: manager.HTTP, RegistryDigests: manager.RegistryDigests}
	if err := restarted.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	rules, err := restarted.AutoPublishRules()
	if err != nil || len(rules) != 1 || rules[0].LastRunID != 11 ||
		rules[0].LastResult != "recovery-required" || rules[0].LastDetail == "" {
		t.Fatalf("missing durable recovery hold: %+v err=%v", rules, err)
	}
	if err := restarted.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 0 {
		t.Fatalf("recovery-required should not auto-redeploy: %v", fake.apps)
	}
	if err := restarted.RedeployManagedApp(context.Background(), rule.App); err != nil {
		t.Fatal(err)
	}
	if err := restarted.MarkManualAutoPublishRecovery(context.Background(), rule.App); err != nil {
		t.Fatal(err)
	}
	// A newer release must present a different immutable manifest after
	// manual recovery synchronizes the previously missed release baseline.
	restarted.RegistryDigests.(*fakeRegistryDigest).nextDigest = otherDigest
	if err := restarted.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 2 || fake.apps[0] != "file-ingest" || fake.apps[1] != "file-ingest" {
		t.Fatalf("expected manual reconciliation then one new release, got %v", fake.apps)
	}
	rules, err = restarted.AutoPublishRules()
	if err != nil || rules[0].LastRunID != 12 || rules[0].LastResult != "deployed" {
		t.Fatalf("new release not persisted after recovery: %+v err=%v", rules, err)
	}
}

func TestAutoPublishFailedClaimBlocksNewReleases(t *testing.T) {
	manager, fake, _ := newAutoTestManager(t)
	rule := AutoPublishRule{
		App: "file-ingest", Repository: "eutelic/file-ingest",
		Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest",
		Branch: "main", LastRunID: 10, LastAttempt: 1,
	}
	if err := manager.SetAutoPublish(rule); err != nil {
		t.Fatal(err)
	}
	meta := autoTestRun(11, "success")
	if claimed, err := manager.claimPublication(rule, meta, "deploying"); err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	if err := manager.publicationResult(rule, meta, "failed", errors.New("TrueNAS unavailable")); err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{meta: autoTestRun(12, "success"), found: true}
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 0 {
		t.Fatalf("later run bypassed failed claim: %v", fake.apps)
	}
	rules, err := manager.AutoPublishRules()
	if err != nil || rules[0].LastResult != "failed" || rules[0].LastRunID != 11 {
		t.Fatalf("failed release cursor lost: %+v err=%v", rules, err)
	}
	// This is called by the authenticated manual-redeploy endpoint after a
	// successful TrueNAS response, never as part of an automatic poll.
	if err := manager.RedeployManagedApp(context.Background(), rule.App); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkManualAutoPublishRecovery(context.Background(), rule.App); err != nil {
		t.Fatal(err)
	}
	manager.RegistryDigests.(*fakeRegistryDigest).nextDigest = otherDigest
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if len(fake.apps) != 2 {
		t.Fatalf("expected manual recovery and exactly one new publication: %v", fake.apps)
	}
}


func TestAutoPublishFailClosedMissingUnchangedAndMismatchedDigest(t *testing.T) {
	for _, tc := range []struct{
		name string
		missing bool
		unchanged bool
		mismatch bool
	}{
		{name: "missing commit digest", missing: true},
		{name: "unchanged latest manifest digest", unchanged: true},
		{name: "latest differs from commit tag", mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, fake, _ := newAutoTestManager(t)
			registry := manager.RegistryDigests.(*fakeRegistryDigest)
			rule := AutoPublishRule{
				App: "file-ingest", Repository: "eutelic/file-ingest",
				Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest",
				Branch: "main", LastRunID: 10, LastAttempt: 1,
			}
			if err := manager.SetAutoPublish(rule); err != nil { t.Fatal(err) }
			registry.missingSHA = tc.missing
			registry.unchanged = tc.unchanged
			registry.mismatch = tc.mismatch
			publisher := &fakePublisher{meta: autoTestRun(11, "success"), found: true}
			for i := 0; i < 2; i++ {
				if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil { t.Fatal(err) }
			}
			if len(fake.apps) != 0 { t.Fatalf("unverified image was deployed: %v", fake.apps) }
			rules, err := manager.AutoPublishRules()
			if err != nil || len(rules) != 1 || rules[0].LastRunID != 10 ||
				rules[0].LastResult != "blocked-image-digest" || rules[0].LastDetail == "" ||
				rules[0].LastImageDigest != baselineDigest {
				t.Fatalf("unverified publication did not fail closed: %+v %v", rules, err)
			}
		})
	}
}

func TestAutoPublishRegistryChangesDuringDeploymentRequiresManualRecovery(t *testing.T) {
	manager, fake, _ := newAutoTestManager(t)
	registry := manager.RegistryDigests.(*fakeRegistryDigest)
	rule := AutoPublishRule{
		App: "file-ingest", Repository: "eutelic/file-ingest",
		Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest",
		Branch: "main", LastRunID: 10, LastAttempt: 1,
	}
	if err := manager.SetAutoPublish(rule); err != nil { t.Fatal(err) }
	registry.mutateAfterCheck = true
	publisher := &fakePublisher{meta: autoTestRun(11, "success"), found: true}
	if err := manager.AutoPublishOnce(context.Background(), publisher); err == nil {
		t.Fatal("expected post-deployment registry mismatch error")
	}
	if len(fake.apps) != 1 { t.Fatalf("expected only first deployment: %v", fake.apps) }
	rules, err := manager.AutoPublishRules()
	if err != nil || rules[0].LastResult != "recovery-required" ||
		rules[0].LastImageDigest != baselineDigest {
		t.Fatalf("ambiguous deployment not fenced: %+v err=%v", rules, err)
	}
	publisher.meta = autoTestRun(12, "success")
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil { t.Fatal(err) }
	if len(fake.apps) != 1 { t.Fatalf("new release bypassed recovery hold: %v", fake.apps) }
}

func TestAutoPublishRecordsVerifiedImageDigest(t *testing.T) {
	manager, fake, _ := newAutoTestManager(t)
	rule := AutoPublishRule{
		App: "file-ingest", Repository: "eutelic/file-ingest",
		Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest",
		Branch: "main", LastRunID: 10, LastAttempt: 1,
	}
	if err := manager.SetAutoPublish(rule); err != nil { t.Fatal(err) }
	rules, err := manager.AutoPublishRules()
	if err != nil || len(rules) != 1 || rules[0].LastImageDigest != baselineDigest {
		t.Fatalf("missing initial immutable baseline: %+v %v", rules, err)
	}
	publisher := &fakePublisher{meta: autoTestRun(11, "success"), found: true}
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil { t.Fatal(err) }
	rules, err = manager.AutoPublishRules()
	if err != nil || len(fake.apps) != 1 || rules[0].LastImageDigest != updatedDigest ||
		rules[0].LastResult != "deployed" {
		t.Fatalf("verified digest not durably recorded: %+v, apps=%v, err=%v", rules, fake.apps, err)
	}
}


func TestManualAutoPublishRecoveryRebasesPinnedTrueNASRelease(t *testing.T) {
	manager, fake, _ := newAutoTestManager(t)
	rule := AutoPublishRule{
		App: "file-ingest", Repository: "eutelic/file-ingest",
		Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest",
		Branch: "main", LastRunID: 10, LastAttempt: 1,
	}
	if err := manager.SetAutoPublish(rule); err != nil { t.Fatal(err) }
	meta := autoTestRun(11, "success")
	if claim, err := manager.claimPublication(rule, meta, "deploying"); err != nil || !claim {
		t.Fatalf("claim=%t error=%v", claim, err)
	}
	if err := manager.publicationResult(rule, meta, "recovery-required", errors.New("HTTP disconnect after app.update")); err != nil {
		t.Fatal(err)
	}
	// The TrueNAS app may have applied the new pinned digest even though the
	// control-plane request timed out and left its cursor unacknowledged.
	for i := range fake.inventory {
		if fake.inventory[i].Name == "file-ingest" {
			fake.inventory[i].Images = []string{"ghcr.io/eutelic/file-ingest@"+updatedDigest}
		}
	}
	if err := manager.RedeployManagedApp(context.Background(), rule.App); err != nil { t.Fatal(err) }
	if err := manager.MarkManualAutoPublishRecovery(context.Background(), rule.App); err != nil { t.Fatal(err) }
	rules, err := manager.AutoPublishRules()
	if err != nil || len(rules) != 1 || rules[0].LastImageDigest != updatedDigest ||
		rules[0].LastResult != "recovered-manually" {
		t.Fatalf("stale pinned baseline after recovery: rules=%+v err=%v",rules,err)
	}
	manager.RegistryDigests.(*fakeRegistryDigest).nextDigest = otherDigest
	publisher := &fakePublisher{meta: autoTestRun(12, "success"), found: true}
	if err := manager.AutoPublishOnce(context.Background(), publisher); err != nil { t.Fatal(err) }
	rules, err = manager.AutoPublishRules()
	if err != nil || rules[0].LastImageDigest != otherDigest || rules[0].LastResult != "deployed" ||
		len(fake.apps) != 2 {
		t.Fatalf("post-reconciliation publishing failed: rules=%+v apps=%v err=%v",rules,fake.apps,err)
	}
}

func TestManualAutoPublishRecoveryRefusesUnverifiableTrueNASImages(t *testing.T) {
	manager, fake, _ := newAutoTestManager(t)
	rule := AutoPublishRule{
		App: "file-ingest", Repository: "eutelic/file-ingest",
		Workflow: "publish-images.yml", Image: "ghcr.io/eutelic/file-ingest:latest",
		Branch: "main", LastRunID: 10, LastAttempt: 1,
	}
	if err := manager.SetAutoPublish(rule); err != nil { t.Fatal(err) }
	meta := autoTestRun(11, "success")
	if claim, err := manager.claimPublication(rule, meta, "deploying"); err != nil || !claim { t.Fatalf("claim=%t err=%v",claim,err) }
	if err := manager.publicationResult(rule,meta,"failed",errors.New("updater interrupted")); err != nil { t.Fatal(err) }
	for i := range fake.inventory {
		if fake.inventory[i].Name == "file-ingest" { fake.inventory[i].Images = []string{"ghcr.io/eutelic/unrelated:latest"} }
	}
	if err := manager.MarkManualAutoPublishRecovery(context.Background(), rule.App); err == nil {
		t.Fatal("cleared recovery hold without verifying current TrueNAS image")
	}
	rules, err := manager.AutoPublishRules()
	if err != nil || rules[0].LastResult != "failed" || rules[0].LastImageDigest != baselineDigest {
		t.Fatalf("invalid automatic recovery after unverifiable app: %+v err=%v",rules,err)
	}
}
