package review

import (
	"encoding/json"
	"fmt"
	"time"
)

// Status は、レビューパイプラインの最終状態です。
type Status string

// パイプラインの最終状態です。
//
// **値は受け取り側の表示分岐や保存済みの記録に現れるため、変更できません。**
// SKIPPED を SUCCESS へ畳まないのは、「差分が無くて何も公開していない」場合と
// 成果物がある場合を呼び出し側が判別できなくなるためです（Result.Published も参照）。
const (
	StatusSucceeded Status = "SUCCESS"
	StatusSkipped   Status = "SKIPPED"
	StatusFailed    Status = "FAILURE"
)

// Result は、レビューパイプラインの最終結果です。
type Result struct {
	Status     Status
	StorageURI string
	PublicURL  string
	Duration   time.Duration
	Message    string
}

// Published は、成果物がストレージへ公開されたかどうかを返します。
func (r Result) Published() bool { return r.Status == StatusSucceeded }

// resultJSON は Result のワイヤ表現です。所要時間を秒（float64）で出す旧来の形式を保ちつつ、
// Go 側では time.Duration として扱うために変換をここへ閉じ込めます。
type resultJSON struct {
	Status          Status  `json:"status"`
	StorageURI      string  `json:"storage_uri"`
	PublicURL       string  `json:"public_url"`
	DurationSeconds float64 `json:"duration_seconds"`
	Message         string  `json:"message"`
}

// MarshalJSON は Result を JSON へ変換します。
func (r Result) MarshalJSON() ([]byte, error) {
	return json.Marshal(resultJSON{
		Status:          r.Status,
		StorageURI:      r.StorageURI,
		PublicURL:       r.PublicURL,
		DurationSeconds: r.Duration.Seconds(),
		Message:         r.Message,
	})
}

// UnmarshalJSON は JSON から Result を復元します。
func (r *Result) UnmarshalJSON(data []byte) error {
	var raw resultJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = Result{
		Status:     raw.Status,
		StorageURI: raw.StorageURI,
		PublicURL:  raw.PublicURL,
		Duration:   time.Duration(raw.DurationSeconds * float64(time.Second)),
		Message:    raw.Message,
	}
	return nil
}

// Succeeded は、レビュー結果を公開できたときの Result を生成します。
func Succeeded(req Request, duration time.Duration) Result {
	return newResult(req, StatusSucceeded, duration, "AIレビューが正常に完了しました。")
}

// Skipped は、差分が無くレビューを行わなかったときの Result を生成します。
func Skipped(req Request, duration time.Duration) Result {
	return newResult(req, StatusSkipped, duration, "差分が存在しないため、レビューをスキップしました。")
}

// Failed は、パイプラインが失敗したときの Result を生成します。
func Failed(req Request, duration time.Duration, err error) Result {
	message := fmt.Sprintf("レビューパイプラインでエラーが発生しました: %v", err)
	return newResult(req, StatusFailed, duration, message)
}

func newResult(req Request, status Status, duration time.Duration, message string) Result {
	return Result{
		Status:     status,
		StorageURI: req.StorageURI,
		PublicURL:  req.PublicURL,
		Duration:   duration,
		Message:    message,
	}
}
