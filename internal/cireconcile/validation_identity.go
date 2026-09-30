package cireconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/ciaudit"
	"github.com/eutelic/runnerhub/internal/ciaudit/workflowparser"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
	"github.com/eutelic/runnerhub/internal/images"
	"github.com/eutelic/runnerhub/internal/store"
)

const externalValidationTTL = 24 * time.Hour

const (
	validationExclusionMissingMetadata       = "missing-workflow-metadata"
	validationExclusionUnsupportedIsolation  = "unsupported-isolation"
	validationExclusionUnpinnedRunnerImage   = "unpinned-runner-image"
	validationExclusionMissingHostContext    = "missing-host-context"
	validationExclusionInvalidRepository     = "invalid-repository"
	validationExclusionWorkflowParse         = "workflow-parse"
	validationExclusionRuntimeEnvironment    = "runtime-environment"
	validationExclusionMutableAction         = "mutable-action"
	validationExclusionOpaqueAction          = "opaque-action"
	validationExclusionUnresolvedContext     = "unresolved-context"
	validationExclusionWorkflowUnclassified  = "workflow-not-classified"
	validationExclusionSideEffectingWorkflow = "side-effecting-workflow"
)

var errValidationIdentityUnsupported = errors.New("validation identity is not safely derivable")

type validationIdentityUnsupportedError struct {
	reason string
	cause  error
}

func (e *validationIdentityUnsupportedError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.reason, e.cause)
	}
	return e.reason
}

func (e *validationIdentityUnsupportedError) Unwrap() error {
	return errValidationIdentityUnsupported
}

func unsupportedValidation(reason string, cause error) error {
	return &validationIdentityUnsupportedError{reason: reason, cause: cause}
}

func validationExclusionReason(err error) (string, bool) {
	var unsupported *validationIdentityUnsupportedError
	if !errors.As(err, &unsupported) || strings.TrimSpace(unsupported.reason) == "" {
		return "", false
	}
	return unsupported.reason, true
}

func (r *Reconciler) persistValidationExclusion(ctx context.Context, worker core.Worker, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil
	}
	if worker.ValidationExclusionAnalysisVersion == ciaudit.AnalyzerVersion && worker.ValidationExclusionReason == reason {
		return nil
	}
	worker.ValidationExclusionAnalysisVersion = ciaudit.AnalyzerVersion
	worker.ValidationExclusionReason = reason
	return r.Store.UpsertWorker(ctx, worker)
}

func sortWorkersByCompletion(workers []core.Worker) {
	sort.SliceStable(workers, func(i, j int) bool {
		left := workers[i].CompletedAt
		if left.IsZero() {
			left = workers[i].FailedAt
		}
		right := workers[j].CompletedAt
		if right.IsZero() {
			right = workers[j].FailedAt
		}
		if left.Equal(right) {
			return workers[i].ID < workers[j].ID
		}
		return left.Before(right)
	})
}

func (r *Reconciler) reconcileValidationIdentity(
	ctx context.Context,
	worker core.Worker,
	run store.CIWorkflowRun,
	certificates store.CIValidationCertificateStore,
	findings store.CIFindingStore,
	fileCache map[string]githubrunner.RepositoryFile,
) error {
	job, ok := ciaudit.ExecutionFromWorker(worker)
	if !ok {
		return nil
	}
	identity, updatedWorker, err := r.validationIdentity(ctx, worker, run, fileCache)
	if errors.Is(err, errValidationIdentityUnsupported) {
		reason, ok := validationExclusionReason(err)
		if !ok {
			reason = validationExclusionMissingMetadata
		}
		return r.persistValidationExclusion(ctx, worker, reason)
	}
	if err != nil {
		return err
	}
	if updatedWorker {
		if err := r.Store.UpsertWorker(ctx, worker); err != nil {
			return err
		}
	}

	key, err := identity.Key()
	if err != nil {
		return err
	}
	repository := normalizeRepository(worker.Repository)
	if prior, err := certificates.GetCIValidationCertificate(ctx, repository, key); err == nil {
		if finding, ok := ciaudit.EquivalentValidationFinding(job, identity, prior, externalValidationTTL); ok {
			finding.Evidence = append(finding.Evidence,
				fmt.Sprintf("definition hash %s", identity.DefinitionHash),
				fmt.Sprintf("input tree hash %s", identity.InputHash),
				fmt.Sprintf("context hash %s", identity.ContextHash),
			)
			if err := findings.UpsertCIFinding(ctx, repository, finding); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	if !strings.EqualFold(strings.TrimSpace(worker.JobResult), "success") {
		return nil
	}
	certificate := ciaudit.ValidationCertificate{
		Key:            key,
		Result:         "success",
		CompletedAt:    job.CompletedAt,
		DefinitionHash: identity.DefinitionHash,
		InputHash:      identity.InputHash,
		ContextHash:    identity.ContextHash,
		Freshness:      identity.Freshness,
	}
	return certificates.UpsertCIValidationCertificate(ctx, repository, certificate)
}

func (r *Reconciler) validationIdentity(
	ctx context.Context,
	worker core.Worker,
	run store.CIWorkflowRun,
	fileCache map[string]githubrunner.RepositoryFile,
) (ciaudit.ValidationIdentity, bool, error) {
	if identity, ok := cachedWorkerIdentity(worker); ok {
		return identity, false, nil
	}
	if strings.TrimSpace(run.HeadTreeSHA) == "" ||
		strings.TrimSpace(run.HeadSHA) == "" ||
		strings.TrimSpace(run.Path) == "" ||
		strings.TrimSpace(worker.JobName) == "" ||
		strings.TrimSpace(worker.GitHubRunnerOS) == "" ||
		strings.TrimSpace(worker.HostID) == "" {
		return ciaudit.ValidationIdentity{}, false, unsupportedValidation(validationExclusionMissingMetadata, nil)
	}
	if worker.Isolation != core.IsolationContainerTrusted {
		return ciaudit.ValidationIdentity{}, false, unsupportedValidation(validationExclusionUnsupportedIsolation, nil)
	}
	if err := images.ValidatePinned(worker.Image); err != nil {
		return ciaudit.ValidationIdentity{}, false, unsupportedValidation(validationExclusionUnpinnedRunnerImage, nil)
	}

	host, err := r.Store.GetHost(ctx, worker.HostID)
	if err != nil || strings.TrimSpace(host.Architecture) == "" {
		return ciaudit.ValidationIdentity{}, false, unsupportedValidation(validationExclusionMissingHostContext, nil)
	}
	scope, ok := repositoryScope(worker.Repository)
	if !ok {
		return ciaudit.ValidationIdentity{}, false, unsupportedValidation(validationExclusionInvalidRepository, nil)
	}
	cacheKey := normalizeRepository(worker.Repository) + ":" + run.HeadSHA + ":" + run.Path
	file, ok := fileCache[cacheKey]
	if !ok {
		file, err = r.GitHub.GetRepositoryFile(ctx, scope, run.Path, run.HeadSHA)
		if err != nil {
			return ciaudit.ValidationIdentity{}, false, err
		}
		fileCache[cacheKey] = file
	}
	fingerprint, err := workflowparser.FingerprintJob(file.Content, worker.JobName)
	if err != nil {
		return ciaudit.ValidationIdentity{}, false, unsupportedValidation(validationExclusionWorkflowParse, err)
	}
	if fingerprint.UsesRuntimeEnvironment {
		return ciaudit.ValidationIdentity{}, false, unsupportedValidation(validationExclusionRuntimeEnvironment, nil)
	}
	if fingerprint.HasMutableAction {
		return ciaudit.ValidationIdentity{}, false, unsupportedValidation(validationExclusionMutableAction, nil)
	}
	if fingerprint.HasOpaqueAction {
		return ciaudit.ValidationIdentity{}, false, unsupportedValidation(validationExclusionOpaqueAction, nil)
	}

	contextHash, err := workflowparser.ContextHash(
		fingerprint.ContextReferences,
		contextValues(run, worker.Repository),
		strings.TrimSpace(worker.JobName),
		strings.ToLower(strings.TrimSpace(worker.GitHubRunnerOS)),
		strings.ToLower(strings.TrimSpace(host.Architecture)),
		string(worker.Isolation),
		strings.TrimSpace(worker.Image),
	)
	if err != nil {
		return ciaudit.ValidationIdentity{}, false, unsupportedValidation(validationExclusionUnresolvedContext, err)
	}
	freshness := ciaudit.FreshnessContentAddressed
	if fingerprint.ExternalState {
		freshness = ciaudit.FreshnessExternal
	}
	identity := ciaudit.ValidationIdentity{
		AnalyzerVersion: ciaudit.AnalyzerVersion,
		DefinitionHash:  fingerprint.DefinitionHash,
		InputHash:       ciaudit.HashParts("git-tree", strings.TrimSpace(run.HeadTreeSHA)),
		ContextHash:     contextHash,
		Freshness:       freshness,
	}
	key, err := identity.Key()
	if err != nil {
		return ciaudit.ValidationIdentity{}, false, err
	}
	worker.ValidationAnalysisVersion = ciaudit.AnalyzerVersion
	worker.ValidationDefinitionHash = identity.DefinitionHash
	worker.ValidationInputHash = identity.InputHash
	worker.ValidationContextHash = identity.ContextHash
	worker.ValidationFreshness = string(identity.Freshness)
	worker.ValidationKey = key
	worker.ValidationWorkflowBlobSHA = file.SHA
	worker.ValidationExclusionAnalysisVersion = ""
	worker.ValidationExclusionReason = ""
	if err := r.Store.UpsertWorker(ctx, worker); err != nil {
		return ciaudit.ValidationIdentity{}, false, err
	}
	return identity, false, nil
}

func cachedWorkerIdentity(worker core.Worker) (ciaudit.ValidationIdentity, bool) {
	if worker.ValidationAnalysisVersion != ciaudit.AnalyzerVersion ||
		strings.TrimSpace(worker.ValidationDefinitionHash) == "" ||
		strings.TrimSpace(worker.ValidationInputHash) == "" ||
		strings.TrimSpace(worker.ValidationContextHash) == "" ||
		strings.TrimSpace(worker.ValidationFreshness) == "" ||
		strings.TrimSpace(worker.ValidationKey) == "" {
		return ciaudit.ValidationIdentity{}, false
	}
	identity := ciaudit.ValidationIdentity{
		AnalyzerVersion: worker.ValidationAnalysisVersion,
		DefinitionHash:  worker.ValidationDefinitionHash,
		InputHash:       worker.ValidationInputHash,
		ContextHash:     worker.ValidationContextHash,
		Freshness:       ciaudit.FreshnessClass(worker.ValidationFreshness),
	}
	key, err := identity.Key()
	return identity, err == nil && key == worker.ValidationKey
}

func contextValues(run store.CIWorkflowRun, repository string) map[string]string {
	values := map[string]string{
		"github.event_name": strings.TrimSpace(run.Event),
		"github.sha":        strings.TrimSpace(run.HeadSHA),
		"github.repository": normalizeRepository(repository),
		"github.workflow":   strings.TrimSpace(run.Name),
	}
	event := strings.ToLower(strings.TrimSpace(run.Event))
	switch event {
	case "push":
		if branch := strings.TrimSpace(run.HeadBranch); branch != "" {
			values["github.ref"] = "refs/heads/" + branch
			values["github.ref_name"] = branch
			values["github.head_ref"] = ""
			values["github.base_ref"] = ""
		}
	case "pull_request":
		if len(run.PullRequestNumbers) == 1 && run.PullRequestNumbers[0] > 0 {
			number := strconv.Itoa(run.PullRequestNumbers[0])
			values["github.ref"] = "refs/pull/" + number + "/merge"
			values["github.ref_name"] = number + "/merge"
			values["github.event.pull_request.number"] = number
		}
		values["github.head_ref"] = strings.TrimSpace(run.HeadBranch)
	}
	return values
}
