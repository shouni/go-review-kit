package publish

import (
	"context"
	"html/template"
	"io"
	"strings"
	"testing"
)

func TestNewConverterRendersDocument(t *testing.T) {
	converter, err := NewConverter()
	if err != nil {
		t.Fatalf("Converter の生成に失敗: %v", err)
	}

	writer := &fakeWriter{}
	publisher, err := New(writer, converter, WithClock(fixedClock()), WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("Publisher の生成に失敗: %v", err)
	}
	if err := publisher.Publish(context.Background(), testRequest(), testReport()); err != nil {
		t.Fatalf("公開に失敗: %v", err)
	}

	html := writer.gotBody

	// 完全な HTML ドキュメントとして出力されます。
	for _, want := range []string{"<html", "<head", "<body", "</html>"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML に %s が含まれていません", want)
		}
	}

	// レポートの内容とメタ情報が本文に載ります。
	for _, want := range []string{
		"レビュー結果",
		"概ね良好です。",
		"未使用の変数です。",
		"main.go:12",
		"ssh://git@github.com/shouni/example.git",
		"2026/08/10 21:30:00 JST",
		"軽微な指摘あり",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML に %q が含まれていません", want)
		}
	}

	// レビュー固有のスタイルは <head> 側へまとめます。本文に <style> が
	// 混ざると、フラグメントとして再利用したときに崩れます。
	head, _, _ := strings.Cut(html, "<body")
	if !strings.Contains(head, ".verdict-Minor") {
		t.Error("レビュー固有の CSS が <head> にありません")
	}
	if strings.Contains(bodyOf(t, html), "<style") {
		t.Error("<body> 内に <style> が混ざっています")
	}
}

// 判定に応じて見出しの文言とクラス名が切り替わります。
func TestConverterRendersVerdicts(t *testing.T) {
	tests := []struct {
		decision string
		want     string
	}{
		{"None", "問題なし"},
		{"Minor", "軽微な指摘あり"},
		{"Major", "要修正"},
		{"Blocker", "ブロッカーあり"},
	}

	converter, err := NewConverter()
	if err != nil {
		t.Fatalf("Converter の生成に失敗: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.decision, func(t *testing.T) {
			content := []byte(`{"title":"t","summary":"s","verdict":{"decision":"` + tt.decision + `","reason":"r"},"findings":[]}`)

			html := runConverter(t, converter, content)
			if !strings.Contains(html, tt.want) {
				t.Errorf("HTML に %q が含まれていません", tt.want)
			}
			if !strings.Contains(html, "verdict-"+tt.decision) {
				t.Errorf("HTML に verdict-%s が含まれていません", tt.decision)
			}
		})
	}
}

// 指摘が無い場合、指摘一覧そのものを出しません。
func TestConverterOmitsEmptyFindings(t *testing.T) {
	converter, err := NewConverter()
	if err != nil {
		t.Fatalf("Converter の生成に失敗: %v", err)
	}

	html := runConverter(t, converter,
		[]byte(`{"title":"t","summary":"s","verdict":{"decision":"None","reason":"r"},"findings":[]}`))

	// クラス名は <head> のスタイル定義にも現れるため、本文だけを見ます。
	if strings.Contains(bodyOf(t, html), "review-findings") {
		t.Error("指摘が無いのに一覧が出力されています")
	}
}

// テンプレートに埋め込まれる値はエスケープされます。AI の出力や差分の抜粋が
// そのまま HTML として解釈されると、成果物のページが壊れます。
func TestConverterEscapesContent(t *testing.T) {
	converter, err := NewConverter()
	if err != nil {
		t.Fatalf("Converter の生成に失敗: %v", err)
	}

	html := runConverter(t, converter, []byte(`{
		"title":"t","summary":"s",
		"verdict":{"decision":"Minor","reason":"r"},
		"findings":[{"severity":"Minor","file":"a.go","excerpt":"<script>alert(1)</script>","message":"m"}]
	}`))

	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Error("抜粋がエスケープされずに出力されています")
	}
}

func TestNewConverterWithTemplate(t *testing.T) {
	tmpl := template.Must(template.New("custom").Parse(`<p id="custom">{{.title}}</p>`))

	converter, err := NewConverterWithTemplate(tmpl, ".custom { color: red; }")
	if err != nil {
		t.Fatalf("Converter の生成に失敗: %v", err)
	}

	html := runConverter(t, converter,
		[]byte(`{"title":"差し替え","summary":"s","verdict":{"decision":"None","reason":"r"},"findings":[]}`))

	if !strings.Contains(html, `<p id="custom">差し替え</p>`) {
		t.Errorf("差し替えたテンプレートが使われていません:\n%s", html)
	}
	if !strings.Contains(html, ".custom { color: red; }") {
		t.Error("差し替えた CSS が使われていません")
	}
}

func TestConverterRejectsInvalidJSON(t *testing.T) {
	converter, err := NewConverter()
	if err != nil {
		t.Fatalf("Converter の生成に失敗: %v", err)
	}

	if _, err := converter.Run([]byte(`{"title":`)); err == nil {
		t.Fatal("エラーを期待しましたが nil でした")
	}
}

// bodyOf は、HTML ドキュメントの <body> 以降を返します。
func bodyOf(t *testing.T, html string) string {
	t.Helper()

	_, body, found := strings.Cut(html, "<body")
	if !found {
		t.Fatal("<body> が見つかりません")
	}
	return body
}

func runConverter(t *testing.T, converter Converter, content []byte) string {
	t.Helper()

	reader, err := converter.Run(content)
	if err != nil {
		t.Fatalf("変換に失敗: %v", err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("読み出しに失敗: %v", err)
	}
	return string(body)
}
