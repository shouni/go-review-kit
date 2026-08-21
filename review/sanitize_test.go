package review

import (
	"encoding/json"
	"strings"
	"testing"
)

// 既に解釈できる入力は 1 バイトも変えないこと。
// 変えてしまうと、正常系まで補修の影響を受けます。
func TestSanitizeJSONLeavesValidInputAlone(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		`{"title":"a"}`,
		`[{"a":1},{"b":2}]`,
		`{"excerpt":"win\\path"}`,
		`{"excerpt":"改行\nタブ\t引用\""}`,
		`  {"title":"a"}  `,
	} {
		if got := string(SanitizeJSON([]byte(in))); got != in {
			t.Errorf("SanitizeJSON(%q) = %q, 変えないでください", in, got)
		}
	}
}

// モデルが混ぜがちな崩れを補修できること。
func TestSanitizeJSONRepairs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{name: "Markdown フェンス", in: "```json\n{\"title\":\"a\"}\n```"},
		{name: "末尾の説明文", in: `{"title":"a"} 以上がレビュー結果です。`},
		{name: "余分な閉じ括弧", in: `{"title":"a"}}`},
		{name: "先頭の前置き", in: `結果は次のとおりです: {"title":"a"}`},
		// ★ 今回踏んだ形。ソースの正規表現を excerpt へ引用すると出ます。
		{name: "エスケープし忘れ（\\d）", in: `{"excerpt":"regexp.MustCompile(\d+)"}`},
		{name: "エスケープし忘れ（空白）", in: `{"excerpt":"path\ name"}`},
		// ★ 複数行の引用。改行をエスケープせずに入れてくる形です。
		{name: "生の改行", in: "{\"excerpt\":\"一行目\n二行目\"}"},
		{name: "生のタブ", in: "{\"excerpt\":\"\tif err != nil {\"}"},
		{name: "生の改行とフェンス", in: "```json\n{\"excerpt\":\"一行目\n二行目\"}\n```"},
		{name: "生の改行とバックスラッシュ", in: "{\"excerpt\":\"re := regexp.MustCompile(\\d+)\n次の行\"}"},
		{name: "配列ルート", in: "```json\n[{\"a\":1}]\n```"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := SanitizeJSON([]byte(tt.in))
			if !json.Valid(got) {
				t.Fatalf("補修後も解釈できません: %s", got)
			}
		})
	}
}

// 補修しても妥当にならないものは入力のまま返すこと。
// 悪化させないのが要点で、呼び出し側のエラーが元の壊れ方を指したままになります。
func TestSanitizeJSONGivesUpCleanly(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		`まったく JSON ではありません`,
		`{"title":`,
		``,
	} {
		if got := string(SanitizeJSON([]byte(in))); got != in {
			t.Errorf("SanitizeJSON(%q) = %q, 直せないなら入力のまま返してください", in, got)
		}
	}
}

// エスケープの補修が中身を変えないこと。
// バックスラッシュは literal として残り、正しいエスケープは解釈されたままになります。
func TestEscapeRepairPreservesContent(t *testing.T) {
	t.Parallel()

	var v struct {
		Excerpt string `json:"excerpt"`
	}
	in := `{"excerpt":"re := regexp.MustCompile(\d+)\nnext"}`
	if err := json.Unmarshal(SanitizeJSON([]byte(in)), &v); err != nil {
		t.Fatalf("補修後に解釈できません: %v", err)
	}
	if !strings.Contains(v.Excerpt, `\d+`) {
		t.Errorf("バックスラッシュが literal として残っていません: %q", v.Excerpt)
	}
	if !strings.Contains(v.Excerpt, "\n") {
		t.Errorf("正しい \\n が literal に化けています: %q", v.Excerpt)
	}
}

// 生の制御文字を補修しても、引用の中身は失われないこと。
//
// 引用は指摘の位置を示す唯一の手掛かり（行番号は省略されうる）なので、
// 直った代わりに中身が変わるのでは意味がありません。
func TestControlCharRepairPreservesContent(t *testing.T) {
	t.Parallel()

	var v struct {
		Excerpt string `json:"excerpt"`
	}
	in := "{\"excerpt\":\"一行目\n\tインデントされた二行目\"}"
	if err := json.Unmarshal(SanitizeJSON([]byte(in)), &v); err != nil {
		t.Fatalf("補修後に解釈できません: %v", err)
	}
	if v.Excerpt != "一行目\n\tインデントされた二行目" {
		t.Errorf("引用の中身が変わりました: %q", v.Excerpt)
	}
}

// 文字列の外の改行・インデントは触らないこと。
// 整形済みの JSON まで書き換えると、補修が「直す」より「壊す」側へ回ります。
func TestControlCharRepairKeepsFormatting(t *testing.T) {
	t.Parallel()

	// 値としては壊れている（末尾に余分な閉じ括弧）が、整形の改行は正当な入力。
	in := "{\n  \"title\": \"a\",\n  \"summary\": \"s\"\n}}"
	got := string(SanitizeJSON([]byte(in)))
	if strings.Contains(got, `\n  "title"`) {
		t.Errorf("整形の改行までエスケープされました: %q", got)
	}
	if !json.Valid([]byte(got)) {
		t.Errorf("補修後も解釈できません: %q", got)
	}
}

// ParseReport が補修を経由して成立すること。
func TestParseReportRecoversFromModelNoise(t *testing.T) {
	t.Parallel()

	raw := "```json\n" + `{"title":"レビュー","summary":"要約","verdict":{"decision":"Minor","reason":"r"},` +
		`"findings":[{"severity":"Minor","file":"x.go","excerpt":"regexp.MustCompile(\d+)","message":"m"}]}` +
		"\n```"

	report, err := ParseReport([]byte(raw))
	if err != nil {
		t.Fatalf("ParseReport() = %v, want nil", err)
	}
	if report.Title != "レビュー" {
		t.Errorf("Title = %q", report.Title)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(report.Findings))
	}
	if !strings.Contains(report.Findings[0].Excerpt, `\d+`) {
		t.Errorf("excerpt = %q", report.Findings[0].Excerpt)
	}
}

// 直しようが無い入力では、元の壊れ方を指すエラーを返すこと。
func TestParseReportKeepsOriginalError(t *testing.T) {
	t.Parallel()

	_, err := ParseReport([]byte(`{"title":`))
	if err == nil {
		t.Fatal("エラーを期待しました")
	}
	if !strings.Contains(err.Error(), "JSONとして解釈できません") {
		t.Errorf("err = %v", err)
	}
}
