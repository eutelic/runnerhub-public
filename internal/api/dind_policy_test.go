package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/policy"
	"github.com/example-user/runnerhub/internal/store"
)

func TestCreateDinDPoolRequiresPrivilegedPolicy(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	payload := `{
		"id":"pool-dind",
		"name":"docker-dind",
		"scope":{"kind":"repository","owner":"acme","repository":"app"},
		"labels":["runnerhub-dind"],
		"isolation":"container-dind",
		"resources":{"cpuMillis":2000,"memoryBytes":4294967296,"diskBytes":21474836480},
		"minIdle":0,
		"maxConcurrency":1,
		"runnerGroupId":1,
		"image":"img"
	}`

	svc := &Service{
		Store: st,
		Policy: policy.Engine{AllowedImages: map[string]struct{}{"img": {}}},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pools", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected DinD pool to be forbidden by default; status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := st.GetPool(ctx, "pool-dind"); err == nil {
		t.Fatal("forbidden DinD pool was persisted")
	}

	svc.Policy.AllowPrivileged = true
	req = httptest.NewRequest(http.MethodPost, "/api/v1/pools", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected DinD pool to be created after explicit opt-in; status=%d body=%s", rr.Code, rr.Body.String())
	}
	pool, err := st.GetPool(ctx, "pool-dind")
	if err != nil {
		t.Fatal(err)
	}
	if pool.Isolation != core.IsolationContainerDinD {
		t.Fatalf("stored isolation=%q", pool.Isolation)
	}
}
