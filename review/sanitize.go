package review

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SanitizeJSON は、AI が返した JSON をそのままでは解釈できないときに補修します。
//
// 構造化出力（ResponseSchema / OutputSchema）を指定しても、モデルは次の形で崩すことが
// あります。**どれも応答を返しきったあとの話なので、API の再試行では直りません。**
//
//   - Markdown のフェンス（```json … ```）で包む
//   - 完結した JSON の後ろに説明文や余分な閉じ括弧を継ぎ足す
//   - 文字列の中でバックスラッシュをエスケープし忘れる（ソースを引用したとき）
//   - 文字列の中に改行やタブを生のまま入れる（複数行を引用したとき）
//
// **既に解釈できる入力は 1 バイトも変えません。** 補修しても妥当な JSON にならなければ
// 入力をそのまま返します。呼び出し側のエラーメッセージが元の壊れ方を指したままになるよう、
// 悪化させないことを優先します。
//
// 補修したかどうかを知りたい場合は、戻り値と入力を比べてください。
//
// **go-gemini-client の gemini.CleanJSONResponse が同じ問題を解いています。意図的に
// 別実装のままです。** このモジュールは engine を差し替えられるよう go.mod を go-git
// 1 本に絞っており、Gemini 専用のクライアントを require すると呼び出し側（adk-review）の
// モジュールグラフに載ります。片方を直したらもう片方も見てください。
// なお salvageJSON（切り詰められた応答からの救出）はこちらにしかありません。
// データを落とす操作で、落ちたことを呼び出し側へ伝える口（ParseInfo.Truncated）が要るためです。
func SanitizeJSON(data []byte) []byte {
	if json.Valid(data) {
		return data
	}

	if repaired := firstJSONValue(data); repaired != nil {
		return repaired
	}

	// 文字列の中の壊れたエスケープは、括弧の対応では直りません。
	// バックスラッシュと生の制御文字を補ってからもう一度、値の切り出しを試みます。
	//
	// 順序に意味があります。制御文字の補修は `\n` のようにバックスラッシュを**足す**ので、
	// 後から裸のバックスラッシュを探すと、足したばかりのエスケープを二重にしてしまいます。
	escaped := escapeControlChars(escapeLoneBackslashes(data))
	if json.Valid(escaped) {
		return escaped
	}
	if repaired := firstJSONValue(escaped); repaired != nil {
		return repaired
	}

	return data
}

// firstJSONValue は、最初に現れる完結した JSON 値だけを取り出します。
// 取り出せなければ nil を返します。
//
// json.Decoder は文字列リテラルの中の括弧を正しく読み飛ばしたうえで、値が閉じた位置で
// 止まります。前後に付いたフェンスや説明文を、括弧を数えずに落とせるのが要点です。
func firstJSONValue(data []byte) []byte {
	start := firstJSONStart(data)
	if start < 0 {
		return nil
	}

	var value json.RawMessage
	if err := json.NewDecoder(strings.NewReader(string(data[start:]))).Decode(&value); err != nil {
		return nil
	}
	return value
}

// firstJSONStart は、最初に現れる JSON 値の開始位置を返します。
// トップレベルが配列のスキーマにも対応するため、'{' と '[' の早い方を採ります。
func firstJSONStart(data []byte) int {
	obj := strings.IndexByte(string(data), '{')
	arr := strings.IndexByte(string(data), '[')

	switch {
	case obj < 0:
		return arr
	case arr < 0:
		return obj
	case obj < arr:
		return obj
	default:
		return arr
	}
}

// escapeLoneBackslashes は、文字列リテラルの中でエスケープされていない
// バックスラッシュを二重にします。
//
// JSON でバックスラッシュの後に来られるのは "\/bfnrtu だけなので、それ以外が続く `\X` は
// **モデルがエスケープし忘れた literal なバックスラッシュ**と一意に決まります
// （ソースの正規表現 `\d` やパスを excerpt へ引用したときに出ます）。`\X` → `\\X` は
// 情報を落とさず、解釈が二通りになることもありません。
//
// 文字列の外は触りません。JSON の構文としてバックスラッシュが現れるのは文字列の中だけです。
func escapeLoneBackslashes(data []byte) []byte {
	var b strings.Builder
	b.Grow(len(data))

	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]

		if !inString {
			if c == '"' {
				inString = true
			}
			b.WriteByte(c)
			continue
		}

		switch c {
		case '"':
			inString = false
			b.WriteByte(c)
		case '\\':
			if i+1 < len(data) && isJSONEscape(data[i+1]) {
				// 正しいエスケープはそのまま通します。ここで 2 バイト進めるのが要点で、
				// `\\` を 1 バイトずつ見ると 2 つ目を裸のバックスラッシュと誤認します。
				b.WriteByte(c)
				b.WriteByte(data[i+1])
				i++
				continue
			}
			// 続きが無効なエスケープ（あるいは末尾）なので、literal として二重にします。
			b.WriteString(`\\`)
		default:
			b.WriteByte(c)
		}
	}
	return []byte(b.String())
}

// escapeControlChars は、文字列リテラルの中に生のまま置かれた制御文字を
// エスケープ表記へ置き換えます。
//
// JSON は文字列の中に U+0020 未満の文字をそのまま置くことを許しません。にもかかわらず
// モデルは、**複数行の引用（excerpt）やインデントを含むコードを貼るときに生の改行・タブを
// 入れてきます。** 構造化出力を指定していても起こり、応答を返しきったあとの崩れなので
// API の再試行では直りません。ここで直さないと、数分から十数分かけたレビューが
// 「解釈できません」の一行だけを残して丸ごと失われます。
//
// 置き換えは一意です。生の制御文字はその位置に現れてはいけない文字なので、エスケープ表記へ
// 写しても意味は変わらず、解釈が二通りになることもありません。文字列の外は触りません
// （JSON の整形に使われた改行やインデントを壊さないためです）。
func escapeControlChars(data []byte) []byte {
	var b strings.Builder
	b.Grow(len(data))

	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]

		if !inString {
			if c == '"' {
				inString = true
			}
			b.WriteByte(c)
			continue
		}

		switch {
		case c == '"':
			inString = false
			b.WriteByte(c)
		case c == '\\':
			// 正しいエスケープは 2 バイトまとめて通します。1 バイトずつ見ると、
			// `\"` の引用符を文字列の終わりと誤認して以降の判定がずれます。
			b.WriteByte(c)
			if i+1 < len(data) && isJSONEscape(data[i+1]) {
				b.WriteByte(data[i+1])
				i++
			}
		case c < 0x20:
			b.WriteString(controlEscape(c))
		default:
			b.WriteByte(c)
		}
	}
	return []byte(b.String())
}

// controlEscape は、制御文字に対する JSON のエスケープ表記を返します。
//
// 短い表記があるものはそちらを使います。ログや成果物を人が読むことがあるので、
// 改行が `\u000a` として並ぶより `\n` の方が読めます。
func controlEscape(c byte) string {
	switch c {
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	default:
		return fmt.Sprintf(`\u%04x`, c)
	}
}

// isJSONEscape は、バックスラッシュの直後に置ける文字かどうかを返します。
//
// \u は続く 4 桁が 16 進である必要がありますが、そこまでは見ません。壊れた \u を
// 直す一意な方法が無く、触ると別の壊し方をするだけだからです。
func isJSONEscape(c byte) bool {
	switch c {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
		return true
	default:
		return false
	}
}

// salvageJSON は、途中で切れた JSON から、完結している範囲だけを取り出します。
// 取り出せなければ nil を返します。
//
// ★ **これは補修ではなく切り捨てです。** SanitizeJSON が「解釈できる入力は 1 バイトも
// 変えない・悪化させない」を守るのに対し、こちらは**データを落とします。** 分けてあるのは
// そのためで、呼び出し側が落ちたことを知らないまま完全な結果として扱えないよう、
// ParseReport は ParseInfo.Truncated で必ず知らせます。
//
// 出力の上限に当たったモデルは、文の途中でぷつりと止まります。そこまでに書けていた
// 指摘は正しい JSON として並んでいるので、**最後に閉じ終えた要素まで戻して、開いたままの
// 括弧を閉じれば読めます。** 実測では、10.7 KiB の差分に対して 212 KB を書いた末に切れ、
// 完成していた Blocker の指摘ごと失われた例があります。
//
// 戻る先は必ず文字列の外です。壊れたエスケープや閉じていない引用符を持ち帰りません。
func salvageJSON(data []byte) []byte {
	start := firstJSONStart(data)
	if start < 0 {
		return nil
	}

	var stack []byte
	// lastSafe は、入れ子の値を閉じ終えた直後の位置です。ここまで戻れば、要素の途中でも
	// 文字列の途中でもない地点に着きます。
	lastSafe := -1
	var stackAtSafe int

	inString, escaped := false, false
	for i := start; i < len(data); i++ {
		c := data[i]

		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) == 0 {
				return nil
			}
			stack = stack[:len(stack)-1]
			// 一番外側が閉じたなら切れていません。救出の出番ではないので譲ります。
			if len(stack) == 0 {
				return nil
			}
			lastSafe, stackAtSafe = i+1, len(stack)
		}
	}

	if lastSafe < 0 {
		return nil
	}

	out := make([]byte, 0, lastSafe-start+stackAtSafe)
	out = append(out, data[start:lastSafe]...)
	for i := stackAtSafe - 1; i >= 0; i-- {
		if stack[i] == '{' {
			out = append(out, '}')
		} else {
			out = append(out, ']')
		}
	}
	if !json.Valid(out) {
		return nil
	}
	return out
}
