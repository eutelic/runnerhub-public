package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/store"
)

func TestHostSchedulingAPI(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertHost(ctx, core.Host{ID: "h1", Name: "compute", State: core.HostOnline}); err != nil {
		t.Fatal(err)
	}
	handler := (&Service{Store: st}).Handler()
	request := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/hosts/"+id+"/scheduling", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}
	if rr := request("h1", `{"schedulingDisabled":true}`); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"schedulingDisabled":true`) {
		t.Fatalf("disable failed: %d %s", rr.Code, rr.Body.String())
	}
	if rr := request("h1", `{"schedulingDisabled":false}`); rr.Code != 200 || strings.Contains(rr.Body.String(), `"schedulingDisabled":true`) {
		t.Fatalf("enable failed: %d %s", rr.Code, rr.Body.String())
	}
	if rr := request("h1", `{}`); rr.Code != 400 {
		t.Fatalf("missing value status %d", rr.Code)
	}
	if rr := request("h1", `{"schedulingDisabled":"yes"}`); rr.Code != 400 {
		t.Fatalf("invalid value status %d", rr.Code)
	}
	if rr := request("missing", `{"schedulingDisabled":true}`); rr.Code != 404 {
		t.Fatalf("unknown host status %d", rr.Code)
	}
	audits, err := st.ListAudit(ctx, 10)
	if err != nil || len(audits) < 2 {
		t.Fatalf("missing toggle audit events: %+v %v", audits, err)
	}
}
