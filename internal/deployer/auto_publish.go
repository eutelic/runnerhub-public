package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
)

var autoRepositoryPattern = regexp.MustCompile("^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$")

// AutoPublishRule is a server-side, opt-in mapping between a GitHub publisher
// and a TrueNAS app. lastRunID/lastAttempt are durable processing cursors:
// enabling the switch starts with the current latest run as baseline, rather
// than unexpectedly re-deploying an old release.
type AutoPublishRule struct {
	App             string    `json:"app"`
	Repository      string    `json:"repository"`
	Workflow        string    `json:"workflow"`
	Image           string    `json:"image"`
	Branch          string    `json:"branch"`
	LastRunID       int64     `json:"lastRunId"`
	LastAttempt     int       `json:"lastAttempt"`
	LastImageDigest string    `json:"lastImageDigest,omitempty"`
	LastResult      string    `json:"lastResult,omitempty"`
	LastDetail      string    `json:"lastDetail,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty"`
}

type autoRulesFile struct {
	Rules []AutoPublishRule `json:"rules"`
}

func validateAutoRule(rule AutoPublishRule) error {
	if !appNamePattern.MatchString(rule.App) || len(rule.App) > 40 {
		return errors.New("invalid auto-deployment app")
	}
	if !autoRepositoryPattern.MatchString(rule.Repository) || strings.Contains(rule.Repository, "..") {
		return errors.New("repository must be owner/name")
	}
	if err := githubrunner.ValidatePublicationWorkflow(rule.Workflow); err != nil {
		return err
	}
	if rule.Branch != "main" {
		return errors.New("auto redeploy currently requires a main-branch publishing workflow")
	}
	if !ImageMatchesPublicationRepository(rule.Image, rule.Repository) {
		return errors.New("automatic publishing requires an app image matching ghcr.io/<publishing-repository>:latest")
	}
	if rule.LastImageDigest != "" && !ValidImageDigest(rule.LastImageDigest) {
		return errors.New("invalid saved GHCR image digest")
	}
	if rule.LastRunID < 0 || rule.LastAttempt < 0 {
		return errors.New("invalid publication cursor")
	}
	return nil
}

// The publishing run is only eligible to trigger a redeploy of the matching
// GHCR image. An unrelated repository cannot publish an update to another app.
func ImageMatchesPublicationRepository(image, repository string) bool {
	expected := "ghcr.io/" + strings.ToLower(strings.TrimSpace(repository)) + ":latest"
	return strings.EqualFold(strings.TrimSpace(image), expected)
}

func (m *BootstrapManager) autoRulesPath() string {
	root := strings.TrimSpace(m.Root)
	if root == "" {
		root = defaultBootstrapRoot
	}
	return filepath.Join(root, "config", "automatic-publishing.json")
}

func (m *BootstrapManager) readAutoRulesLocked() (autoRulesFile, error) {
	var file autoRulesFile
	data, err := os.ReadFile(m.autoRulesPath())
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, err
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, fmt.Errorf("parse automatic publishing settings: %w", err)
	}
	seen := map[string]bool{}
	for _, rule := range file.Rules {
		if err := validateAutoRule(rule); err != nil {
			return autoRulesFile{}, fmt.Errorf("invalid saved auto rule for %s: %w", rule.App, err)
		}
		if seen[rule.App] {
			return autoRulesFile{}, errors.New("duplicate auto-deployment app")
		}
		seen[rule.App] = true
	}
	return file, nil
}

func (m *BootstrapManager) saveAutoRulesLocked(file autoRulesFile) error {
	sort.Slice(file.Rules, func(i, j int) bool { return file.Rules[i].App < file.Rules[j].App })
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	path := m.autoRulesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'))
}

func (m *BootstrapManager) AutoPublishRules() ([]AutoPublishRule, error) {
	m.autoMu.Lock()
	defer m.autoMu.Unlock()
	file, err := m.readAutoRulesLocked()
	return file.Rules, err
}

// SetAutoPublish stores the verified GitHub mapping. A newly enabled rule
// baselines the latest observed run even if it was successful, preventing a
// surprise redeployment of a historical image at the moment of opt-in.
func (m *BootstrapManager) imageDigest(ctx context.Context, image string) (string, error) {
	resolver := m.RegistryDigests
	if resolver == nil {
		resolver = GHCRDigestResolver{Client: m.HTTP}
	}
	return resolver.Digest(ctx, image)
}

func (m *BootstrapManager) SetAutoPublish(rule AutoPublishRule) error {
	if err := validateAutoRule(rule); err != nil {
		return err
	}
	// Baseline the actual registry digest when the operator opts in. A
	// successful workflow alone cannot prove an image changed.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	digest, err := m.imageDigest(ctx, rule.Image)
	if err != nil {
		return fmt.Errorf("cannot enable automatic publishing without a verified GHCR baseline: %w", err)
	}
	if !ValidImageDigest(digest) {
		return errors.New("GHCR baseline must be an immutable SHA-256 digest")
	}
	rule.LastImageDigest = digest
	m.autoMu.Lock()
	defer m.autoMu.Unlock()
	file, err := m.readAutoRulesLocked()
	if err != nil {
		return err
	}
	rule.UpdatedAt = time.Now().UTC()
	rule.LastResult = "watching"
	rule.LastDetail = ""
	found := false
	for i := range file.Rules {
		if file.Rules[i].App == rule.App {
			file.Rules[i] = rule
			found = true
			break
		}
	}
	if !found {
		if len(file.Rules) >= 50 {
			return errors.New("maximum automatic publisher rules reached")
		}
		file.Rules = append(file.Rules, rule)
	}
	return m.saveAutoRulesLocked(file)
}

func (m *BootstrapManager) DisableAutoPublish(app string) error {
	if !appNamePattern.MatchString(app) {
		return errors.New("invalid app")
	}
	m.autoMu.Lock()
	defer m.autoMu.Unlock()
	file, err := m.readAutoRulesLocked()
	if err != nil {
		return err
	}
	keep := file.Rules[:0]
	for _, rule := range file.Rules {
		if rule.App != app {
			keep = append(keep, rule)
		}
	}
	if len(keep) == len(file.Rules) {
		return nil
	}
	file.Rules = keep
	return m.saveAutoRulesLocked(file)
}

func publicationAfter(meta githubrunner.WorkflowRunMetadata, rule AutoPublishRule) bool {
	if meta.ID != rule.LastRunID {
		return meta.ID > rule.LastRunID
	}
	return meta.Attempt > rule.LastAttempt
}

// GitHub run.path commonly includes an @branch suffix. A reusable workflow
// or a run from another repository must not be mistaken for the chosen
// publisher. Compare both the complete path and the branch suffix.
func matchingPublicationPath(path string, rule AutoPublishRule) bool {
	expected := ".github/workflows/" + rule.Workflow
	if path == expected || path == expected+"@"+rule.Branch {
		return true
	}
	// GitHub can return a repo-qualified path for referenced workflows.
	return path == rule.Repository+"/"+expected+"@"+rule.Branch
}

func validPublication(meta githubrunner.WorkflowRunMetadata, rule AutoPublishRule) bool {
	if meta.ID <= 0 || meta.Attempt <= 0 ||
		!strings.EqualFold(meta.HeadBranch, rule.Branch) ||
		!strings.EqualFold(meta.Status, "completed") ||
		!strings.EqualFold(meta.Conclusion, "success") ||
		(meta.Event != "push" && meta.Event != "workflow_dispatch") ||
		!matchingPublicationPath(meta.Path, rule) {
		return false
	}
	if len(meta.HeadSHA) != 40 {
		return false
	}
	for _, c := range meta.HeadSHA {
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// claimPublication records the in-flight release before sending a deployment.
// The persisted "deploying" state is NOT a successful acknowledgement: an
// interrupted operation must be reviewed, never blindly replayed or skipped.
func (m *BootstrapManager) claimPublication(rule AutoPublishRule, meta githubrunner.WorkflowRunMetadata, result string) (bool, error) {
	m.autoMu.Lock()
	defer m.autoMu.Unlock()
	file, err := m.readAutoRulesLocked()
	if err != nil {
		return false, err
	}
	for i := range file.Rules {
		saved := &file.Rules[i]
		if saved.App != rule.App {
			continue
		}
		if saved.Repository != rule.Repository || saved.Workflow != rule.Workflow || saved.Branch != rule.Branch || saved.Image != rule.Image ||
			!publicationAfter(meta, *saved) {
			return false, nil
		}
		saved.LastRunID, saved.LastAttempt = meta.ID, meta.Attempt
		saved.LastResult = result
		saved.LastDetail = ""
		saved.UpdatedAt = time.Now().UTC()
		return true, m.saveAutoRulesLocked(file)
	}
	return false, nil
}

func (m *BootstrapManager) publicationResult(rule AutoPublishRule, meta githubrunner.WorkflowRunMetadata, result string, cause error) error {
	return m.publicationDigestResult(rule, meta, result, cause, "")
}

func (m *BootstrapManager) publicationDigestResult(rule AutoPublishRule, meta githubrunner.WorkflowRunMetadata, result string, cause error, digest string) error {
	if digest != "" && !ValidImageDigest(digest) {
		return errors.New("cannot save invalid deployed image digest")
	}
	m.autoMu.Lock()
	defer m.autoMu.Unlock()
	file, err := m.readAutoRulesLocked()
	if err != nil {
		return err
	}
	for i := range file.Rules {
		saved := &file.Rules[i]
		if saved.App != rule.App || saved.Repository != rule.Repository || saved.Workflow != rule.Workflow || saved.Image != rule.Image ||
			saved.Branch != rule.Branch || saved.LastRunID != meta.ID || saved.LastAttempt != meta.Attempt {
			continue
		}
		saved.LastResult = result
		if result == "deployed" && digest != "" {
			saved.LastImageDigest = digest
		}
		if cause != nil {
			saved.LastDetail = safeDetail(cause)
		} else {
			saved.LastDetail = ""
		}
		saved.UpdatedAt = time.Now().UTC()
		return m.saveAutoRulesLocked(file)
	}
	return nil
}

// A digest mismatch is a recoverable publication verification failure, not
// a deployed or claimed run. Do not advance its cursor: allow the publisher
// to finish indexing its immutable tag, then reevaluate the same run.
func (m *BootstrapManager) blockPublicationDigest(rule AutoPublishRule, detail string) error {
	m.autoMu.Lock()
	defer m.autoMu.Unlock()
	file, err := m.readAutoRulesLocked()
	if err != nil {
		return err
	}
	for i := range file.Rules {
		saved := &file.Rules[i]
		if saved.App != rule.App || saved.Repository != rule.Repository ||
			saved.Workflow != rule.Workflow || saved.Branch != rule.Branch ||
			saved.Image != rule.Image {
			continue
		}
		if saved.LastResult == "failed" || saved.LastResult == "recovery-required" || saved.LastResult == "deploying" {
			return nil
		}
		if saved.LastResult == "blocked-image-digest" && saved.LastDetail == detail {
			return nil
		}
		saved.LastResult = "blocked-image-digest"
		saved.LastDetail = detail
		saved.UpdatedAt = time.Now().UTC()
		return m.saveAutoRulesLocked(file)
	}
	return nil
}

// An automatic redeployment may continue within TrueNAS after an HTTP
// disconnect. Do not replay it, or start a newer release, until an operator
// has reconciled the ambiguous result. The standalone deployer has a ten
// minute deadline, so allow a further five minutes before flagging a stale
// claim. This status survives control-plane restarts.
const autoPublishRecoveryDeadline = 15 * time.Minute

func (m *BootstrapManager) autoPublicationNeedsReview(rule AutoPublishRule, now time.Time) (bool, error) {
	m.autoMu.Lock()
	defer m.autoMu.Unlock()
	file, err := m.readAutoRulesLocked()
	if err != nil {
		return false, err
	}
	for i := range file.Rules {
		saved := &file.Rules[i]
		if saved.App != rule.App {
			continue
		}
		// A replaced or disabled rule must never be changed by a stale poll.
		if saved.Repository != rule.Repository || saved.Workflow != rule.Workflow ||
			saved.Branch != rule.Branch || saved.Image != rule.Image {
			return true, nil
		}
		switch saved.LastResult {
		case "failed", "recovery-required":
			return true, nil
		case "deploying":
			if now.Sub(saved.UpdatedAt) < autoPublishRecoveryDeadline {
				return true, nil
			}
			saved.LastResult = "recovery-required"
			saved.LastDetail = "Automatic deployment outcome is unknown. Inspect TrueNAS deployment history, then use Redeploy now to recover; newer automatic releases are paused."
			saved.UpdatedAt = now
			return true, m.saveAutoRulesLocked(file)
		}
		return false, nil
	}
	return true, nil
}

// MarkManualAutoPublishRecovery is called only after an authenticated manual
// TrueNAS redeploy succeeds. Reconcile the currently installed immutable
// image or take a fresh moving-tag registry baseline before clearing a
// failed/ambiguous automatic publication. Clearing without rebasing could
// mistake an already-pinned release for a stale source image.
func (m *BootstrapManager) MarkManualAutoPublishRecovery(ctx context.Context, app string) error {
	rules, err := m.AutoPublishRules()
	if err != nil {
		return err
	}
	var rule *AutoPublishRule
	for i := range rules {
		if rules[i].App == app &&
			(rules[i].LastResult == "failed" || rules[i].LastResult == "recovery-required") {
			rule = &rules[i]
			break
		}
	}
	if rule == nil {
		return nil
	}

	apps, err := m.ManagedApps(ctx)
	if err != nil {
		return fmt.Errorf("verify TrueNAS app after manual recovery: %w", err)
	}
	base := strings.TrimSuffix(strings.ToLower(rule.Image), ":latest")
	observed := ""
	for _, candidate := range apps {
		if candidate.Name != app {
			continue
		}
		if !candidate.Enabled || candidate.Protected || !candidate.CustomApp || candidate.State != "RUNNING" {
			return errors.New("manual recovery did not confirm a running, enabled custom TrueNAS app")
		}
		for _, installed := range candidate.Images {
			ref := strings.ToLower(strings.TrimSpace(installed))
			if ref == strings.ToLower(rule.Image) || strings.HasPrefix(ref, base+"@sha256:") {
				if observed != "" {
					return errors.New("manual recovery found ambiguous matching TrueNAS images")
				}
				observed = ref
			}
		}
	}
	if observed == "" {
		return errors.New("manual recovery cannot verify the app's running GHCR image")
	}
	baseline := ""
	if observed == strings.ToLower(rule.Image) {
		// A legacy :latest app requires a fresh current baseline after its
		// explicit operator-initiated pull/redeploy completed successfully.
		baseline, err = m.imageDigest(ctx, rule.Image)
		if err != nil {
			return fmt.Errorf("verify GHCR baseline after manual recovery: %w", err)
		}
	} else {
		baseline = strings.TrimPrefix(observed, base+"@")
	}
	if !ValidImageDigest(baseline) {
		return errors.New("manual recovery lacks a valid pinned or registry image digest")
	}

	m.autoMu.Lock()
	defer m.autoMu.Unlock()
	file, err := m.readAutoRulesLocked()
	if err != nil {
		return err
	}
	for i := range file.Rules {
		saved := &file.Rules[i]
		if saved.App != app {
			continue
		}
		if saved.Repository != rule.Repository || saved.Workflow != rule.Workflow ||
			saved.Branch != rule.Branch || saved.Image != rule.Image ||
			saved.LastRunID != rule.LastRunID || saved.LastAttempt != rule.LastAttempt {
			return errors.New("automatic publishing rule changed during manual recovery; review it before enabling")
		}
		if saved.LastResult != "failed" && saved.LastResult != "recovery-required" {
			return errors.New("automatic publishing recovery hold changed during verification")
		}
		saved.LastImageDigest = baseline
		saved.LastResult = "recovered-manually"
		saved.LastDetail = ""
		saved.UpdatedAt = time.Now().UTC()
		return m.saveAutoRulesLocked(file)
	}
	return errors.New("automatic publishing rule disappeared during manual recovery")
}

// AutoPublishOnce polls only explicitly configured publishing workflows.
// The GitHub App must have Actions-read access for each repository. No
// repository-supplied webhook or untrusted workflow payload can trigger it.
func (m *BootstrapManager) AutoPublishOnce(ctx context.Context, github githubrunner.PublicationRunsBackend) error {
	if github == nil {
		return nil
	}
	rules, err := m.AutoPublishRules()
	if err != nil || len(rules) == 0 {
		return err
	}
	apps, err := m.ManagedApps(ctx)
	if err != nil {
		return err
	}
	enabled := map[string]bool{}
	imagesByApp := map[string][]string{}
	for _, app := range apps {
		enabled[app.Name] = app.Enabled && !app.Protected && app.CustomApp
		imagesByApp[app.Name] = app.Images
	}
	var errs []error
	for _, rule := range rules {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !enabled[rule.App] {
			continue
		}
		// Never skip a crashed or failed release by processing a later run.
		// A successful manual redeploy is the explicit recovery action.
		needsReview, reviewErr := m.autoPublicationNeedsReview(rule, time.Now().UTC())
		if reviewErr != nil {
			errs = append(errs, fmt.Errorf("reconcile %s automatic publication: %w", rule.App, reviewErr))
			continue
		}
		if needsReview {
			continue
		}
		parts := strings.SplitN(rule.Repository, "/", 2)
		scope := core.GitHubScope{Kind: core.ScopeRepository, Owner: parts[0], Repository: parts[1]}
		meta, found, lookupErr := github.LatestPublicationRun(ctx, scope, rule.Workflow, rule.Branch)
		if lookupErr != nil {
			errs = append(errs, fmt.Errorf("check %s publication: %w", rule.App, lookupErr))
			continue
		}
		if !found || meta.ID <= 0 || meta.Attempt <= 0 || !publicationAfter(meta, rule) {
			continue
		}
		if !strings.EqualFold(meta.Status, "completed") {
			continue
		}
		imagePresent := false
		previousPinned := strings.TrimSuffix(strings.ToLower(rule.Image), ":latest") + "@" + rule.LastImageDigest
		for _, image := range imagesByApp[rule.App] {
			if strings.EqualFold(strings.TrimSpace(image), rule.Image) ||
				(ValidImageDigest(rule.LastImageDigest) && strings.EqualFold(strings.TrimSpace(image), previousPinned)) {
				imagePresent = true
				break
			}
		}
		if !imagePresent {
			_, claimErr := m.claimPublication(rule, meta, "skipped-image-mismatch")
			if claimErr != nil {
				errs = append(errs, fmt.Errorf("record %s image mismatch: %w", rule.App, claimErr))
			}
			continue
		}
		if !validPublication(meta, rule) {
			claimed, claimErr := m.claimPublication(rule, meta, "skipped-unverified")
			if claimErr != nil {
				errs = append(errs, fmt.Errorf("skip unverified %s publication: %w", rule.App, claimErr))
			}
			_ = claimed
			continue
		}
		// The workflow must publish a full-commit SHA tag and latest to
		// the SAME OCI manifest digest. Neither a successful job nor a
		// mutable tag by itself is an acceptable deployment identity.
		if !ValidImageDigest(rule.LastImageDigest) {
			detail := "Missing registry baseline from an older rule; disable and re-enable auto-redeploy to verify the current image digest."
			if err := m.blockPublicationDigest(rule, detail); err != nil {
				errs = append(errs, fmt.Errorf("record %s missing baseline: %w", rule.App, err))
			}
			continue
		}
		immutableImage := "ghcr.io/" + strings.ToLower(rule.Repository) + ":sha-" + meta.HeadSHA
		immutableDigest, immutableErr := m.imageDigest(ctx, immutableImage)
		latestDigest, latestErr := m.imageDigest(ctx, rule.Image)
		detail := ""
		switch {
		case immutableErr != nil:
			detail = "Publishing run has no verifiable sha-<full commit> GHCR image tag: " + safeDetail(immutableErr)
		case latestErr != nil:
			detail = "Cannot verify the current GHCR :latest manifest digest: " + safeDetail(latestErr)
		case !ValidImageDigest(immutableDigest) || !ValidImageDigest(latestDigest):
			detail = "Registry returned an invalid OCI manifest digest"
		case immutableDigest != latestDigest:
			detail = "The full-commit SHA tag and :latest resolve to different manifest digests; automatic redeploy is blocked"
		case latestDigest == rule.LastImageDigest:
			detail = "Publishing workflow succeeded but the GHCR image manifest digest did not change"
		}
		if detail != "" {
			if err := m.blockPublicationDigest(rule, detail); err != nil {
				errs = append(errs, fmt.Errorf("record %s unverified digest: %w", rule.App, err))
			}
			continue
		}
		claimed, claimErr := m.claimPublication(rule, meta, "deploying")
		if claimErr != nil {
			errs = append(errs, fmt.Errorf("claim %s publication: %w", rule.App, claimErr))
			continue
		}
		if !claimed {
			continue
		}
		if err := m.RedeployManagedAppPinned(ctx, rule.App, rule.Image, rule.LastImageDigest, latestDigest); err != nil {
			// TrueNAS must apply the digest itself, not pull a moving tag.
			// Do not silently lose the release after a failed deployment.
			// The durable failed state blocks future automatic runs until a
			// confirmed manual redeploy resolves the ambiguity.
			if recordErr := m.publicationResult(rule, meta, "failed", fmt.Errorf("%w; review TrueNAS and use Redeploy now to resume automatic publishing", err)); recordErr != nil {
				errs = append(errs, fmt.Errorf("record %s failure: %w", rule.App, recordErr))
			}
			errs = append(errs, fmt.Errorf("auto-redeploy %s: %w", rule.App, err))
			continue
		}
		// If the moving tag changes while TrueNAS pulls/redeploys, the
		// observed result is ambiguous: require operator reconciliation.
		postDigest, postErr := m.imageDigest(ctx, rule.Image)
		if postErr != nil || postDigest != latestDigest {
			detail := errors.New("GHCR :latest changed or became unverifiable during deployment; inspect TrueNAS and recover manually")
			if err := m.publicationResult(rule, meta, "recovery-required", detail); err != nil {
				errs = append(errs, fmt.Errorf("record %s post-deploy mismatch: %w", rule.App, err))
			}
			errs = append(errs, fmt.Errorf("auto-redeploy %s: %w", rule.App, detail))
			continue
		}
		if err := m.publicationDigestResult(rule, meta, "deployed", nil, latestDigest); err != nil {
			errs = append(errs, fmt.Errorf("record %s auto-redeploy: %w", rule.App, err))
		}
	}
	return errors.Join(errs...)
}
