package review

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestResultConstructors(t *testing.T) {
	req := validRequest()
	const duration = 3*time.Second + 500*time.Millisecond

	tests := []struct {
		name          string
		result        Result
		wantStatus    Status
		wantPublished bool
	}{
		{"success", Succeeded(req, duration), StatusSucceeded, true},
		{"skip", Skipped(req, duration), StatusSkipped, false},
		{"failure", Failed(req, duration, errors.New("boom")), StatusFailed, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.result.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", tt.result.Status, tt.wantStatus)
			}
			if tt.result.Published() != tt.wantPublished {
				t.Errorf("Published() = %v, want %v", tt.result.Published(), tt.wantPublished)
			}
			if tt.result.StorageURI != req.StorageURI || tt.result.PublicURL != req.PublicURL {
				t.Errorf("the request's storage location was not carried over: %+v", tt.result)
			}
			if tt.result.Duration != duration {
				t.Errorf("Duration = %v, want %v", tt.result.Duration, duration)
			}
			if tt.result.Message == "" {
				t.Error("Message is empty")
			}
		})
	}
}

func TestFailedResultIncludesCause(t *testing.T) {
	cause := errors.New("API is not responding")

	result := Failed(validRequest(), time.Second, WrapStep(StepReview, cause))
	if want := cause.Error(); !strings.Contains(result.Message, want) {
		t.Fatalf("Message does not include the cause: %q", result.Message)
	}
	if !strings.Contains(result.Message, StepReview) {
		t.Fatalf("Message does not include the step name: %q", result.Message)
	}
}
