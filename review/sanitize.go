package review

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SanitizeJSON repairs JSON returned by the AI when it cannot be interpreted as-is.
//
// Even with structured output (ResponseSchema / OutputSchema) specified, the model can break the
// output in the following ways. All of them happen after the response has been fully returned, so
// retrying the API call does not fix them.
//
//   - Wrapping it in a Markdown fence (```json … ```)
//   - Appending remarks or extra closing brackets after a complete JSON value
//   - Forgetting to escape a backslash inside a string (when quoting source)
//   - Putting raw newlines or tabs inside a string (when quoting multiple lines)
//
// Input that can already be interpreted is not changed by a single byte. If the repair does not
// produce valid JSON, the input is returned unchanged. Never making things worse takes priority, so
// that the caller's error message keeps pointing at the original breakage.
//
// To find out whether a repair happened, compare the return value with the input.
//
// go-gemini-client's gemini.CleanJSONResponse solves the same problem. This stays a separate
// implementation on purpose. This module keeps go.mod down to go-git alone so the engine can be
// swapped, and requiring a Gemini-specific client would put it in the caller's (adk-review's)
// module graph. If you fix one, look at the other too.
// salvageJSON (recovery from a truncated response) exists only here, though: it is an operation
// that drops data, and it needs a way (ParseInfo.Truncated) to tell the caller that data was
// dropped.
func SanitizeJSON(data []byte) []byte {
	if json.Valid(data) {
		return data
	}

	if repaired := firstJSONValue(data); repaired != nil {
		return repaired
	}

	// Broken escapes inside strings are not fixed by matching brackets.
	// Fix backslashes and raw control characters, then try extracting the value again.
	//
	// The order matters. Repairing control characters adds backslashes, as in `\n`, so looking for
	// bare backslashes afterwards would double the escapes just added.
	escaped := escapeControlChars(escapeLoneBackslashes(data))
	if json.Valid(escaped) {
		return escaped
	}
	if repaired := firstJSONValue(escaped); repaired != nil {
		return repaired
	}

	return data
}

// firstJSONValue extracts only the first complete JSON value.
// It returns nil when none can be extracted.
//
// json.Decoder correctly skips brackets inside string literals and stops where the value closes.
// The point is that surrounding fences and remarks can be dropped without counting brackets.
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

// firstJSONStart returns the start position of the first JSON value.
// It takes whichever of '{' and '[' comes first, to also support schemas whose top level is an
// array.
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

// escapeLoneBackslashes doubles backslashes that are not escaped inside string literals.
//
// In JSON only "\/bfnrtu may follow a backslash, so a `\X` followed by anything else is
// unambiguously a literal backslash the model forgot to escape (it shows up when a regular
// expression such as `\d` or a path from the source is quoted into an excerpt). `\X` → `\\X`
// drops no information and cannot be read two ways.
//
// Nothing outside strings is touched. In JSON syntax backslashes appear only inside strings.
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
				// Valid escapes pass through unchanged. Advancing 2 bytes here is the point:
				// looking at `\\` one byte at a time would mistake the second for a bare backslash.
				b.WriteByte(c)
				b.WriteByte(data[i+1])
				i++
				continue
			}
			// What follows is not a valid escape (or this is the end), so double it as a literal.
			b.WriteString(`\\`)
		default:
			b.WriteByte(c)
		}
	}
	return []byte(b.String())
}

// escapeControlChars replaces control characters placed raw inside string literals with their
// escape notation.
//
// JSON does not allow characters below U+0020 to appear raw inside a string. Even so, the model
// puts raw newlines and tabs in when it pastes a multi-line quote (excerpt) or indented code. It
// happens even with structured output specified, and since the breakage comes after the response
// has been fully returned, retrying the API call does not fix it. Without a fix here, a review that
// took several to more than ten minutes is lost entirely, leaving only a one-line "cannot parse".
//
// The replacement is unambiguous. A raw control character must not appear at that position, so
// mapping it to its escape notation does not change the meaning and cannot be read two ways.
// Nothing outside strings is touched (so as not to break the newlines and indentation used to
// format the JSON).
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
			// Valid escapes pass through as 2 bytes together. Looking one byte at a time would
			// mistake the quote in `\"` for the end of the string and throw off everything after.
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

// controlEscape returns the JSON escape notation for a control character.
//
// Where a short notation exists it is used. People read logs and artifacts, and `\n` reads
// better than newlines lined up as `\u000a`.
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

// isJSONEscape reports whether c may directly follow a backslash.
//
// \u requires the following 4 digits to be hexadecimal, but that is not checked. There is no
// unambiguous way to fix a broken \u, and touching it would only break it differently.
func isJSONEscape(c byte) bool {
	switch c {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
		return true
	default:
		return false
	}
}

// salvageJSON extracts only the completed part of a JSON value that was cut off.
// It returns nil when nothing can be extracted.
//
// ★ This is truncation, not repair. SanitizeJSON keeps to "never change interpretable input by a
// single byte, never make it worse", while this function drops data. That is why the two are
// separate: so the caller cannot treat the result as complete without knowing data was dropped,
// ParseReport always reports it through ParseInfo.Truncated.
//
// A model that hits its output limit stops abruptly mid-sentence. The findings written up to that
// point sit there as valid JSON, so going back to the last element that was closed and closing the
// brackets still open makes it readable. In one measured case the output was cut off after writing
// 212 KB for a 10.7 KiB diff, losing a completed Blocker finding along with it.
//
// The point returned to is always outside a string. No broken escape or unclosed quote is carried
// back.
func salvageJSON(data []byte) []byte {
	start := firstJSONStart(data)
	if start < 0 {
		return nil
	}

	var stack []byte
	// lastSafe is the position right after a nested value was closed. Going back to it lands at a
	// point that is neither in the middle of an element nor in the middle of a string.
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
			// If the outermost value closed, nothing was cut off. Salvage has no job here, so step aside.
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
