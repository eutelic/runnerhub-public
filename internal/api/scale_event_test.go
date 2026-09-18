package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eutelic/runnerhub/internal/store"
)

func TestScaleEventUnknownTerminalIsIdempotent(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	svc := &Service{Store: st}
	for _, state := range []string{"completed", "canceled", "failed"} {
		t.Run(state, func(t *testing.T) {
			body := `{"poolId":"pool-publisher","runnerName":"runner-missing","state":"` + state + `"}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", strings.NewReader(body))
			rec := httptest.NewRecorder()

			svc.scaleEvent(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestScaleEventUnknownBusyStillFails(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	svc := &Service{Store: st}
	body := `{"poolId":"pool-publisher","runnerName":"runner-missing","state":"busy"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scale-events", strings.NewReader(body))
	rec := httptest.NewRecorder()

	svc.scaleEvent(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
}
