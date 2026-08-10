package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shouni/go-remote-io/remoteio"

	"github.com/shouni/go-review-kit/review"
)

func testRequest() review.Request {
	return review.Request{
		RepoURL:    "ssh://git@github.com/shouni/example.git",
		Base:       "main",
		Head:       "develop",
		Mode:       "detail",
		Model:      "gemini-2.5-pro",
		StorageURI: "gs://bucket/review.html",
		PublicURL:  "https://example.com/review.html",
	}
}

func testReport() review.Report {
	return review.Report{
		Title:   "レビュー結果",
		Summary: "概ね良好です。",
		Verdict: review.Verdict{Decision: review.DecisionMinor, Reason: "軽微な指摘が1件"},
		Findings: []review.Finding{
			{
				Severity:   review.SeverityMinor,
				File:       "main.go",
				Line:       12,
				Excerpt:    "x := 1",
				Message:    "未使用の変数です。",
				Suggestion: "削除してください。",
			},
		},
	}
}

// fixedClock は、出力を固定するためのテスト用の時計です。
func fixedClock() func() time.Time {
	return func() time.Time {
		return time.Date(2026, 8, 10, 21, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeWriter は remoteio.Writer のテスト実装です。
type fakeWriter struct {
	err error

	called   bool
	gotPath  string
	gotBody  string
	gotOpts  []remoteio.WriteOption
	gotCtxOK bool
}

func (f *fakeWriter) Write(ctx context.Context, path string, r io.Reader, opts ...remoteio.WriteOption) error {
	f.called = true
	f.gotPath = path
	f.gotOpts = opts
	f.gotCtxOK = ctx.Err() == nil

	if f.err != nil {
		return f.err
	}

	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.gotBody = string(body)
	return nil
}

// fakeConverter は Converter のテスト実装です。受け取った JSON をそのまま保持します。
type fakeConverter struct {
	err error

	gotContent []byte
}

func (f *fakeConverter) Run(content []byte) (io.Reader, error) {
	f.gotContent = content
	if f.err != nil {
		return nil, f.err
	}
	return bytes.NewReader([]byte("<html>変換結果</html>")), nil
}

func TestNewValidatesDeps(t *testing.T) {
	tests := []struct {
		name      string
		writer    remoteio.Writer
		converter Converter
	}{
		{"writer が nil", nil, &fakeConverter{}},
		{"converter が nil", &fakeWriter{}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.writer, tt.converter); err == nil {
				t.Fatal("エラーを期待しましたが nil でした")
			}
		})
	}

	if _, err := New(&fakeWriter{}, &fakeConverter{}); err != nil {
		t.Fatalf("正常な依存で失敗しました: %v", err)
	}
}

func TestPublish(t *testing.T) {
	writer := &fakeWriter{}
	converter := &fakeConverter{}

	publisher, err := New(writer, converter, WithClock(fixedClock()), WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("Publisher の生成に失敗: %v", err)
	}

	if err := publisher.Publish(context.Background(), testRequest(), testReport()); err != nil {
		t.Fatalf("公開に失敗: %v", err)
	}

	if !writer.called {
		t.Fatal("書き込みが行われていません")
	}
	if writer.gotPath != "gs://bucket/review.html" {
		t.Errorf("保存先 = %q", writer.gotPath)
	}
	if writer.gotBody != "<html>変換結果</html>" {
		t.Errorf("本文 = %q", writer.gotBody)
	}
	if len(writer.gotOpts) != 1 {
		t.Errorf("書き込みオプションの数 = %d, want 1 (Content-Type)", len(writer.gotOpts))
	}
}

// 変換へ渡す JSON には、レポート本体に加えて「どのリポジトリのどの範囲をいつ見たか」が
// トップレベルのキーとして載ります。テンプレートはこのキー名を参照します。
func TestPublishBuildsViewJSON(t *testing.T) {
	converter := &fakeConverter{}

	publisher, err := New(&fakeWriter{}, converter, WithClock(fixedClock()), WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("Publisher の生成に失敗: %v", err)
	}

	if err := publisher.Publish(context.Background(), testRequest(), testReport()); err != nil {
		t.Fatalf("公開に失敗: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(converter.gotContent, &got); err != nil {
		t.Fatalf("変換へ渡した JSON をデコードできません: %v", err)
	}

	want := map[string]any{
		"title":          "レビュー結果",
		"summary":        "概ね良好です。",
		"repo_url":       "ssh://git@github.com/shouni/example.git",
		"base_branch":    "main",
		"feature_branch": "develop",
		"reviewed_at":    "2026/08/10 21:30:00 JST",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %v, want %v", key, got[key], value)
		}
	}

	verdict, ok := got["verdict"].(map[string]any)
	if !ok {
		t.Fatalf("verdict が展開されていません: %T", got["verdict"])
	}
	if verdict["decision"] != "Minor" {
		t.Errorf("verdict.decision = %v", verdict["decision"])
	}

	findings, ok := got["findings"].([]any)
	if !ok || len(findings) != 1 {
		t.Fatalf("findings が展開されていません: %v", got["findings"])
	}
}

// 指摘に行番号や修正案が無い場合、キー自体を出しません。テンプレートが
// {{if .line}} で表示を切り替えるため、0 や空文字が載ると誤って描画されます。
func TestPublishOmitsEmptyOptionalFields(t *testing.T) {
	converter := &fakeConverter{}

	publisher, err := New(&fakeWriter{}, converter, WithClock(fixedClock()), WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("Publisher の生成に失敗: %v", err)
	}

	report := testReport()
	report.Findings[0].Line = 0
	report.Findings[0].Suggestion = ""

	if err := publisher.Publish(context.Background(), testRequest(), report); err != nil {
		t.Fatalf("公開に失敗: %v", err)
	}

	var got struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal(converter.gotContent, &got); err != nil {
		t.Fatalf("デコードに失敗: %v", err)
	}

	for _, key := range []string{"line", "suggestion"} {
		if _, ok := got.Findings[0][key]; ok {
			t.Errorf("%s が出力されています", key)
		}
	}
}

func TestPublishErrors(t *testing.T) {
	cause := errors.New("boom")

	tests := []struct {
		name      string
		writer    *fakeWriter
		converter *fakeConverter
		wantWrite bool
	}{
		{
			name:      "変換に失敗",
			writer:    &fakeWriter{},
			converter: &fakeConverter{err: cause},
		},
		{
			name:      "書き込みに失敗",
			writer:    &fakeWriter{err: cause},
			converter: &fakeConverter{},
			wantWrite: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publisher, err := New(tt.writer, tt.converter, WithClock(fixedClock()), WithLogger(discardLogger()))
			if err != nil {
				t.Fatalf("Publisher の生成に失敗: %v", err)
			}

			err = publisher.Publish(context.Background(), testRequest(), testReport())
			if err == nil {
				t.Fatal("エラーを期待しましたが nil でした")
			}
			if !errors.Is(err, cause) {
				t.Fatalf("原因まで辿れません: %v", err)
			}

			// 変換に失敗した時点で書き込みは行いません。
			if tt.writer.called != tt.wantWrite {
				t.Errorf("書き込みの呼び出し = %v, want %v", tt.writer.called, tt.wantWrite)
			}
		})
	}
}

func TestWithClockDefaultsToNow(t *testing.T) {
	converter := &fakeConverter{}

	publisher, err := New(&fakeWriter{}, converter, WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("Publisher の生成に失敗: %v", err)
	}
	if err := publisher.Publish(context.Background(), testRequest(), testReport()); err != nil {
		t.Fatalf("公開に失敗: %v", err)
	}

	var got struct {
		ReviewedAt string `json:"reviewed_at"`
	}
	if err := json.Unmarshal(converter.gotContent, &got); err != nil {
		t.Fatalf("デコードに失敗: %v", err)
	}
	if !strings.HasSuffix(got.ReviewedAt, "JST") {
		t.Errorf("reviewed_at = %q, want 日本時間", got.ReviewedAt)
	}
}

func TestOptionsIgnoreNil(t *testing.T) {
	publisher, err := New(&fakeWriter{}, &fakeConverter{}, WithLogger(nil), WithClock(nil))
	if err != nil {
		t.Fatalf("Publisher の生成に失敗: %v", err)
	}
	if publisher.logger == nil {
		t.Error("logger が nil で上書きされています")
	}
	if publisher.now == nil {
		t.Error("now が nil で上書きされています")
	}
}
