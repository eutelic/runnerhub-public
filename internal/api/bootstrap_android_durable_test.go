package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func postNativeAndroidEnrollment(t *testing.T, svc *Service, extra string) store.Operation {
	t.Helper()
	body := `{"address":"compute.example","user":"compute","name":"compute-android",
        "hostKeyFingerprint":"SHA256:verified","mode":"android-emulator"` + extra + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/bootstrap-operations", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("native enrollment status=%d body=%s", rr.Code, rr.Body.String())
	}
	var op store.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &op); err != nil {
		t.Fatal(err)
	}
	if op.ID == "" {
		t.Fatal("tracked enrollment did not return operation ID")
	}
	return op
}

func awaitNativeAndroidOperation(t *testing.T, st *store.SQLite, id, want string) store.Operation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		op, err := st.GetOperation(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if op.State == want {
			return op
		}
		if op.State == "failed" || op.State == "blocked" || op.State == "succeeded" {
			t.Fatalf("native enrollment state=%q step=%q detail=%q; want %q", op.State, op.Step, op.Detail, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("native enrollment did not reach %q", want)
	return store.Operation{}
}

func TestNativeAndroidModeRejectsUnsafeConfiguration(t *testing.T) {
	base := bootstrapHostRequest{Address: "compute.example", User: "compute", Name: "compute-android",
		HostKeyFingerprint: "SHA256:verified", Mode: "android-emulator"}
	tests := []struct {
		name   string
		change func(*bootstrapHostRequest)
		want   string
	}{
		{"allowed", func(*bootstrapHostRequest) {}, ""},
		{"unknown-mode", func(in *bootstrapHostRequest) { in.Mode = "untrusted" }, "unsupported machine setup mode"},
		{"privileged", func(in *bootstrapHostRequest) { in.AllowPrivilegedWorkers = true }, "cannot enable publisher/DinD"},
		{"spoofed-kvm", func(in *bootstrapHostRequest) { in.Tags = []string{"hardware.kvm"} }, "system-verified"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.change(&in)
			err := validateBootstrapInput(&in)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestTrackedNativeAndroidOnboardingPreservesVerifiedCapability(t *testing.T) {
	svc, st, _ := operationService(t)
	defer st.Close()
	capture := &captureMachineBootstrap{}
	svc.MachineBootstrap = capture
	op := postNativeAndroidEnrollment(t, svc, `,"priority":3`)
	final := awaitNativeAndroidOperation(t, st, op.ID, "succeeded")
	if final.HostID != "host-new" || final.Step != "agent-ready" {
		t.Fatalf("final operation=%+v", final)
	}
	if !capture.req.AndroidEmulator || capture.req.HostName != "compute-android" {
		t.Fatalf("wrong native request=%+v", capture.req)
	}
	if capture.req.AllowPrivilegedWorkers {
		t.Fatal("native service must not use Docker publisher privileges")
	}
	host, err := st.GetHost(context.Background(), "host-new")
	if err != nil {
		t.Fatal(err)
	}
	if host.Provider != "process" || host.SystemTags[core.AndroidEmulatorCapabilityTag] != "true" {
		t.Fatalf("system-verified KVM capability not preserved: %+v", host)
	}
}

func TestTrackedNativeAndroidBlocksUnverifiedKVM(t *testing.T) {
	svc, st, _ := operationService(t)
	defer st.Close()
	svc.MachineBootstrap = &captureMachineBootstrap{noVerifiedKVM: true}
	op := postNativeAndroidEnrollment(t, svc, "")
	result := awaitNativeAndroidOperation(t, st, op.ID, "blocked")
	if result.Step != "native-kvm-unverified" || !strings.Contains(result.Detail, "hardware.kvm") {
		t.Fatalf("non-actionable native capability result: %+v", result)
	}
}

func TestNativeAndroidDoesNotReEnrollExistingOnlineName(t *testing.T) {
	svc, st, _ := operationService(t)
	defer st.Close()
	existing := core.Host{ID: "host-existing", Name: "compute-android", Provider: "docker", State: core.HostOnline}
	if err := st.UpsertHost(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	capture := &captureMachineBootstrap{}
	svc.MachineBootstrap = capture
	in := bootstrapHostRequest{Address: "compute.example", User: "compute", Name: "compute-android",
		HostKeyFingerprint: "SHA256:verified", Mode: "android-emulator"}
	if err := validateBootstrapInput(&in); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.runHostBootstrap(context.Background(), in); err == nil || !strings.Contains(err.Error(), "already uses this name") {
		t.Fatalf("online host name reuse must be rejected: %v", err)
	}
	if capture.req.HostName != "" {
		t.Fatal("SSH setup began despite duplicate online host identity")
	}
}
