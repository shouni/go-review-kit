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
		{"成功", Succeeded(req, duration), StatusSucceeded, true},
		{"スキップ", Skipped(req, duration), StatusSkipped, false},
		{"失敗", Failed(req, duration, errors.New("boom")), StatusFailed, false},
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
				t.Errorf("リクエストの保存先が引き継がれていません: %+v", tt.result)
			}
			if tt.result.Duration != duration {
				t.Errorf("Duration = %v, want %v", tt.result.Duration, duration)
			}
			if tt.result.Message == "" {
				t.Error("Message が空です")
			}
		})
	}
}

func TestFailedResultIncludesCause(t *testing.T) {
	cause := errors.New("APIが応答しません")

	result := Failed(validRequest(), time.Second, WrapStep(StepReview, cause))
	if want := cause.Error(); !strings.Contains(result.Message, want) {
		t.Fatalf("Message に原因が含まれていません: %q", result.Message)
	}
	if !strings.Contains(result.Message, StepReview) {
		t.Fatalf("Message に工程名が含まれていません: %q", result.Message)
	}
}
