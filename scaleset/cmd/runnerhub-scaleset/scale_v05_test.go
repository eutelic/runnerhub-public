package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/actions/scaleset"
)

type recordingJobAcquirer struct {
	requested []int64
	err       error
	calls     int
}

func (a *recordingJobAcquirer) AcquireJobs(_ context.Context, ids []int64) ([]int64, error) {
	a.calls++
	a.requested = append([]int64(nil), ids...)
	return nil, a.err
}

func TestScaleV05AcquiresAvailableJobs(t *testing.T) {
	acquirer := &recordingJobAcquirer{}
	s := &scaler{jobs: acquirer}
	message := &scaleset.RunnerScaleSetMessage{
		JobAvailableMessages: []*scaleset.JobAvailable{
			{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 101}},
			nil,
			{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 102}},
		},
	}
	if err := s.Scale(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if acquirer.calls != 1 || !reflect.DeepEqual(acquirer.requested, []int64{101, 102}) {
		t.Fatalf("acquired %v in %d calls; want 101 and 102 once", acquirer.requested, acquirer.calls)
	}
}

func TestScaleV05PreservesRedeliveryWhenAcquireFails(t *testing.T) {
	failed := errors.New("temporary GitHub assignment failure")
	s := &scaler{jobs: &recordingJobAcquirer{err: failed}}
	message := &scaleset.RunnerScaleSetMessage{
		JobAvailableMessages: []*scaleset.JobAvailable{
			{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 77}},
		},
	}
	if err := s.Scale(context.Background(), message); !errors.Is(err, failed) {
		t.Fatalf("Scale must return acquisition errors to prevent message acknowledgment: %v", err)
	}
}

func TestScaleV05DoesNotAcknowledgeJobsWithoutClient(t *testing.T) {
	s := &scaler{}
	message := &scaleset.RunnerScaleSetMessage{
		JobAvailableMessages: []*scaleset.JobAvailable{
			{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 8}},
		},
	}
	if err := s.Scale(context.Background(), message); err == nil {
		t.Fatal("jobs must not be acknowledged when no acquisition client is configured")
	}
}

func TestScaleV05IgnoresEmptyPollAndDoesNotAcquireEmptyJobs(t *testing.T) {
	acquirer := &recordingJobAcquirer{}
	s := &scaler{jobs: acquirer}
	if err := s.Scale(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Scale(context.Background(), &scaleset.RunnerScaleSetMessage{
		JobAvailableMessages: []*scaleset.JobAvailable{nil},
	}); err != nil {
		t.Fatal(err)
	}
	if acquirer.calls != 0 {
		t.Fatalf("unexpected acquisition on empty messages: %d", acquirer.calls)
	}
}
