package review

import (
	"encoding/json"
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

// 所要時間は Go 側では time.Duration、JSON では秒（duration_seconds）で扱います。
func TestResultJSONRoundTrip(t *testing.T) {
	original := Succeeded(validRequest(), 1500*time.Millisecond)

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("エンコードに失敗: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("デコードに失敗: %v", err)
	}
	if got, ok := raw["duration_seconds"].(float64); !ok || got != 1.5 {
		t.Fatalf("duration_seconds = %v, want 1.5", raw["duration_seconds"])
	}
	if raw["status"] != string(StatusSucceeded) {
		t.Fatalf("status = %v, want %v", raw["status"], StatusSucceeded)
	}

	var restored Result
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("復元に失敗: %v", err)
	}
	if restored != original {
		t.Fatalf("往復で一致しません:\n got %+v\nwant %+v", restored, original)
	}
}
