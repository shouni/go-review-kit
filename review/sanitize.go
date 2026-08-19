package review

import (
	"encoding/json"
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
//
// **既に解釈できる入力は 1 バイトも変えません。** 補修しても妥当な JSON にならなければ
// 入力をそのまま返します。呼び出し側のエラーメッセージが元の壊れ方を指したままになるよう、
// 悪化させないことを優先します。
//
// 補修したかどうかを知りたい場合は、戻り値と入力を比べてください。
func SanitizeJSON(data []byte) []byte {
	if json.Valid(data) {
		return data
	}

	if repaired := firstJSONValue(data); repaired != nil {
		return repaired
	}

	// 文字列の中の壊れたエスケープは、括弧の対応では直りません。
	// バックスラッシュを補ってからもう一度、値の切り出しを試みます。
	escaped := escapeLoneBackslashes(data)
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
