package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/gorilla/websocket"
)

// PinnedAppRedeployer intentionally supports only inspectable custom Compose
// applications; catalog/wizard apps cannot be safely rewritten by guessing.
type PinnedAppRedeployer interface {
	RedeployPinnedAndWait(context.Context, string, string, string, string) error
}

type pinnedAppSnapshot struct {
	Name string `json:"name"`
	CustomApp bool `json:"custom_app"`
	State string `json:"state"`
	Config map[string]any `json:"config"`
	ActiveWorkloads struct {
		Images []string `json:"images"`
	} `json:"active_workloads"`
}

// pinCustomCompose leaves all unrelated services/volumes/config intact and
// changes exactly one known image reference. Unexpected config changes must
// be reviewed instead of silently replaced.
func pinCustomCompose(config map[string]any, image, previousDigest, targetDigest string) (map[string]any, string, error) {
	if !ImageMatchesPublicationRepository(image, strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(image), ":latest"), "ghcr.io/")) ||
		!ValidImageDigest(previousDigest) || !ValidImageDigest(targetDigest) {
		return nil, "", errors.New("pinned deployment requires an exact GHCR image and two SHA-256 digests")
	}
	if config == nil {
		return nil, "", errors.New("TrueNAS custom app configuration unavailable")
	}
	// Deep copy through JSON to preserve all configuration entries while
	// avoiding mutation before every validation succeeds.
	raw, err := json.Marshal(config)
	if err != nil { return nil, "", fmt.Errorf("encode current compose configuration: %w", err) }
	var copyConfig map[string]any
	if err := json.Unmarshal(raw, &copyConfig); err != nil {
		return nil, "", fmt.Errorf("decode current compose configuration: %w", err)
	}
	if _, ok := copyConfig["include"]; ok {
		return nil, "", errors.New("Compose include directives require manual review")
	}
	services, ok := copyConfig["services"].(map[string]any)
	if !ok || len(services) == 0 {
		return nil, "", errors.New("TrueNAS app is not an inspectable custom Compose application")
	}
	base := strings.TrimSuffix(strings.ToLower(image), ":latest")
	previousPinned := base+"@"+previousDigest
	targetPinned := base+"@"+targetDigest
	matches := 0
	serviceName := ""
	for name, value := range services {
		svc, ok := value.(map[string]any)
		if !ok { return nil, "", fmt.Errorf("invalid Compose service %q", name) }
		current, hasImage := svc["image"].(string)
		if !hasImage { continue }
		if _, build := svc["build"]; build {
			return nil, "", errors.New("Compose build directives are unsupported for automatic pinned deployment")
		}
		current = strings.TrimSpace(current)
		if strings.EqualFold(current, image) || strings.EqualFold(current, previousPinned) {
			matches++
			svc["image"] = targetPinned
			serviceName = name
		} else if strings.HasPrefix(strings.ToLower(current), base+":") ||
			strings.HasPrefix(strings.ToLower(current), base+"@") {
			return nil, "", fmt.Errorf("service %q changed image outside the verified publishing baseline", name)
		}
	}
	if matches != 1 {
		return nil, "", fmt.Errorf("expected exactly one service using the verified image; found %d", matches)
	}
	return copyConfig, serviceName, nil
}

func (c *TrueNASClient) RedeployPinnedAndWait(ctx context.Context, app, image, previousDigest, targetDigest string) error {
	if !appNamePattern.MatchString(app) || len(app) > 40 {
		return errors.New("invalid TrueNAS app")
	}
	if !ValidImageDigest(previousDigest) || !ValidImageDigest(targetDigest) ||
		previousDigest == targetDigest {
		return errors.New("pinned deployment needs a verified changed image digest")
	}
	conn, _, err := c.dialer.DialContext(ctx, c.endpoint, nil)
	if err != nil { return fmt.Errorf("connect to TrueNAS: %w", err) }
	defer conn.Close()
	if err := c.authenticatePinnedCall(ctx, conn); err != nil { return err }
	readApp := func() (pinnedAppSnapshot, error) {
		var snapshot pinnedAppSnapshot
		err := c.call(ctx, conn, "app.get_instance", []any{app, map[string]any{
			"extra": map[string]any{"retrieve_config": true},
		}}, &snapshot)
		return snapshot, err
	}
	before, err := readApp()
	if err != nil { return fmt.Errorf("inspect TrueNAS app before pinned deployment: %w", err) }
	if before.Name != app || !before.CustomApp || before.State != "RUNNING" {
		return errors.New("digest-pinned redeployment requires a running custom TrueNAS app")
	}
	updated, serviceName, err := pinCustomCompose(before.Config, image, previousDigest, targetDigest)
	if err != nil { return err }
	// app.update replaces the whole custom Compose document. Re-read before
	// submitting to catch changes made while resolving/validating the source.
	// This is a best-effort stale-write guard, NOT an atomic compare-and-swap:
	// a concurrent operator edit after this check can still be lost.
	stillCurrent, err := readApp()
	if err != nil { return fmt.Errorf("recheck TrueNAS app before pinned update: %w", err) }
	if stillCurrent.Name != app || !stillCurrent.CustomApp || stillCurrent.State != "RUNNING" ||
		!reflect.DeepEqual(before.Config, stillCurrent.Config) {
		return errors.New("TrueNAS app configuration changed before pinned update; refusing to overwrite concurrent edits")
	}
	// app.update is a job. TrueNAS waits for the replacement config and
	// Compose to apply before its JSON-RPC result is returned.
	if err := c.call(ctx, conn, "app.update", []any{app, map[string]any{
		"custom_compose_config": updated,
	}}, nil); err != nil {
		return fmt.Errorf("update TrueNAS app to pinned digest: %w", err)
	}
	after, err := readApp()
	if err != nil { return fmt.Errorf("verify pinned TrueNAS app: %w", err) }
	ref := strings.TrimSuffix(strings.ToLower(image), ":latest")+"@"+targetDigest
	if after.Name != app || !after.CustomApp || after.State != "RUNNING" {
		return errors.New("TrueNAS app did not return to RUNNING after pinned deployment")
	}
	services, ok := after.Config["services"].(map[string]any)
	if !ok { return errors.New("TrueNAS no longer returns verifiable Compose services") }
	svc, ok := services[serviceName].(map[string]any)
	if !ok || !strings.EqualFold(fmt.Sprint(svc["image"]), ref) {
		return errors.New("TrueNAS stored an image different from the verified immutable digest")
	}
	if len(after.ActiveWorkloads.Images) == 0 {
		return errors.New("TrueNAS did not report the active container image")
	}
	observed := false
	for _, active := range after.ActiveWorkloads.Images {
		if strings.EqualFold(active, ref) { observed = true; break }
	}
	if !observed {
		return errors.New("TrueNAS running image does not match the requested immutable digest")
	}
	return nil
}

func (c *TrueNASClient) authenticatePinnedCall(ctx context.Context, conn *websocket.Conn) error {
	var auth struct { ResponseType string `json:"response_type"` }
	if err := c.call(ctx, conn, "auth.login_ex", []any{map[string]any{
		"mechanism": "API_KEY_PLAIN", "username": c.username, "api_key": c.apiKey,
		"login_options": map[string]any{"user_info": false},
	}}, &auth); err != nil {
		return fmt.Errorf("authenticate to TrueNAS: %w", err)
	}
	if auth.ResponseType != "SUCCESS" {
		return errors.New("TrueNAS authentication failed")
	}
	return nil
}
