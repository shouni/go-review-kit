package review

import (
	"encoding/json"
	"testing"
)

// FuzzSanitizeJSON は、補修が入力を悪化させないことを確かめます。
//
// 不変条件は 2 つです。入力を 1 バイトでも変えたなら、その結果は妥当な JSON でなければ
// なりません（直せないなら手を出さない）。そして既に妥当な入力は 1 バイトも変わりません。
// 補修はバックスラッシュと制御文字を手書きで走査し、括弧の対応を数えるコードで、
// 表のテストは思いついた壊れ方しか試せません。悪化させた場合、呼び出し側のエラーは
// モデルが実際に返したものではなく補修の途中経過を指すことになります。
func FuzzSanitizeJSON(f *testing.F) {
	for _, s := range fuzzJSONSeeds() {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		got := SanitizeJSON(data)

		if json.Valid(data) {
			if string(got) != string(data) {
				t.Fatalf("valid input was altered:\n in: %q\nout: %q", data, got)
			}
			return
		}
		if string(got) == string(data) {
			return // 直せなかったのでそのまま返した。許される。
		}
		if !json.Valid(got) {
			t.Fatalf("input was altered but is still invalid:\n in: %q\nout: %q", data, got)
		}
	})
}

// FuzzParseReport は、ParseReport が成功したレポートが必ず Validate を通ること、
// 切り詰めの報告が実際に切り詰めたときだけ立つことを確かめます。
//
// Truncated を取りこぼすと、途中で切れたレビューが完全なものとして公開されます。
func FuzzParseReport(f *testing.F) {
	for _, s := range fuzzJSONSeeds() {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		report, info, err := ParseReport(data)
		if err != nil {
			return
		}

		// 成功したなら、その内容は必ず検証を通る。
		if err := report.Validate(); err != nil {
			t.Fatalf("ParseReport(%q) succeeded but Validate rejects it: %v", data, err)
		}
		// 元の入力がそのまま解釈できたなら、補修も切り詰めも起きていない。
		if json.Valid(data) && (info.Repaired || info.Truncated) {
			t.Fatalf("ParseReport(%q) reported Repaired=%v Truncated=%v for already-valid input",
				data, info.Repaired, info.Truncated)
		}
	})
}

// fuzzJSONSeeds は、実際に見たモデル出力の壊れ方を並べたものです。
func fuzzJSONSeeds() []string {
	const valid = `{"title":"t","summary":"s","verdict":{"decision":"approve","reason":"r"}}`
	return []string{
		valid,
		"```json\n" + valid + "\n```",
		"Here you go:\n" + valid + "\nHope it helps.",
		valid + "}}}",
		`{"title":"C:\path\to\file"}`,
		"{\"title\":\"line1\nline2\"}",
		"{\"title\":\"tab\there\"}",
		`{"title":"t","findings":[{"file":"a.go"`,
		`{"title":`,
		"[]",
		"{}",
		"null",
		"",
		"not json at all",
		`{"title":"t","summary":"s","verdict":{"decision":"nope"}}`,
	}
}
