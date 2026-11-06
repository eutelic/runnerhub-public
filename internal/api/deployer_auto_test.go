package api

import (
	"testing"

	"github.com/eutelic/runnerhub/internal/githubrunner"
)

func TestInitialPublicationCursor(t *testing.T) {
	for _, tc := range []struct {
		name string
		found bool
		status string
		id int64
		attempt int
		wantID int64
		wantAttempt int
	}{
		{name: "no runs", found: false, wantID: 0, wantAttempt: 0},
		{name: "historical completed success", found: true, status: "completed", id: 42, attempt: 1, wantID: 42, wantAttempt: 1},
		{name: "historical completed failure", found: true, status: "completed", id: 43, attempt: 2, wantID: 43, wantAttempt: 2},
		{name: "publishing currently queued", found: true, status: "queued", id: 44, attempt: 1, wantID: 0, wantAttempt: 0},
		{name: "publishing in progress", found: true, status: "in_progress", id: 45, attempt: 2, wantID: 0, wantAttempt: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, attempt := initialPublicationCursor(githubrunner.WorkflowRunMetadata{
				ID: tc.id, Attempt: tc.attempt, Status: tc.status,
			}, tc.found)
			if id != tc.wantID || attempt != tc.wantAttempt {
				t.Fatalf("cursor=(%d,%d), want (%d,%d)", id, attempt, tc.wantID, tc.wantAttempt)
			}
		})
	}
}
