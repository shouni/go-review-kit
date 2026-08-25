package review

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validRequest() Request {
	return Request{
		RepoURL:    "ssh://git@github.com/shouni/example.git",
		Base:       "main",
		Head:       "develop",
		Mode:       "detail",
		Model:      "gemini-2.5-pro",
		StorageURI: "gs://bucket/review.html",
		PublicURL:  "https://example.com/review.html",
	}
}

func TestRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Request)
		wantErr bool
	}{
		{
			name:   "全項目が揃っている",
			mutate: func(*Request) {},
		},
		{
			name:   "mode は任意",
			mutate: func(r *Request) { r.Mode = "" },
		},
		{
			name:   "public_url は任意",
			mutate: func(r *Request) { r.PublicURL = "" },
		},
		{
			name:    "repo_url が無い",
			mutate:  func(r *Request) { r.RepoURL = "" },
			wantErr: true,
		},
		{
			name:    "base_branch が無い",
			mutate:  func(r *Request) { r.Base = "" },
			wantErr: true,
		},
		{
			name:    "feature_branch が無い",
			mutate:  func(r *Request) { r.Head = "" },
			wantErr: true,
		},
		{
			name:    "model_name が無い",
			mutate:  func(r *Request) { r.Model = "" },
			wantErr: true,
		},
		{
			name:    "storage_uri が無い",
			mutate:  func(r *Request) { r.StorageURI = "" },
			wantErr: true,
		},
		{
			name:    "空白のみは未設定として扱う",
			mutate:  func(r *Request) { r.Base = "   " },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			tt.mutate(&req)

			err := req.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("エラーを期待しましたが nil でした")
				}
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("ErrInvalidRequest を包んでいません: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
		})
	}
}

// 旧実装のペイロード形式（base_branch / feature_branch / model_name）をそのまま
// デコードできることを保証します。フィールド名を Base / Head へ改名したため、
// タグが崩れると流通中のペイロードが静かに空で通ってしまいます。
func TestRequestJSONTagsAreCompatible(t *testing.T) {
	const payload = `{
		"repo_url": "ssh://git@github.com/shouni/example.git",
		"base_branch": "main",
		"feature_branch": "develop",
		"mode": "detail",
		"model_name": "gemini-2.5-pro",
		"storage_uri": "gs://bucket/review.html",
		"public_url": "https://example.com/review.html"
	}`

	var got Request
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("デコードに失敗: %v", err)
	}
	if got != validRequest() {
		t.Fatalf("デコード結果が一致しません: %+v", got)
	}
}

// JobID は呼び出し側の相関IDで、本ライブラリは生成も解釈もしません。
// 必須にすると、ジョブ基盤を持たない呼び出し側が使えなくなります。
func TestRequestJobIDIsOptional(t *testing.T) {
	req := validRequest()
	req.JobID = ""

	if err := req.Validate(); err != nil {
		t.Fatalf("JobID なしで検証に失敗しました: %v", err)
	}

	req.JobID = "20260810-213000-a1b2c3d4"
	if err := req.Validate(); err != nil {
		t.Fatalf("JobID ありで検証に失敗しました: %v", err)
	}
}

// JobID を持たない既存ペイロードは job_id なしでエンコードされます。
// 常に出力すると、この形を読む側に空文字の分岐を強いることになります。
func TestRequestJobIDJSON(t *testing.T) {
	data, err := json.Marshal(validRequest())
	if err != nil {
		t.Fatalf("エンコードに失敗: %v", err)
	}
	if strings.Contains(string(data), "job_id") {
		t.Errorf("JobID が空なのに出力されています: %s", data)
	}

	req := validRequest()
	req.JobID = "20260810-213000-a1b2c3d4"

	data, err = json.Marshal(req)
	if err != nil {
		t.Fatalf("エンコードに失敗: %v", err)
	}

	var restored Request
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("デコードに失敗: %v", err)
	}
	if restored.JobID != req.JobID {
		t.Errorf("JobID = %q, want %q", restored.JobID, req.JobID)
	}
}
