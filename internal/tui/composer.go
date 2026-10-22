package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The composer keeps a byte offset of its insertion point so that ordinary
// typing, multiline editing, and caret movement behave the way a text field
// is expected to behave inside a terminal application.

// clampCursor keeps the insertion point inside the text.
func clampCursor(text string, cursor int) int {
	if cursor < 0 {
		return 0
	}
	if cursor > len(text) {
		return len(text)
	}
	return cursor
}

// insertText inserts text at the insertion point and returns the new text and
// the new insertion point.
func insertText(text string, cursor int, insert string) (string, int) {
	cursor = clampCursor(text, cursor)
	return text[:cursor] + insert + text[cursor:], cursor + len(insert)
}

// deleteBackward removes the character before the insertion point.
func deleteBackward(text string, cursor int) (string, int) {
	cursor = clampCursor(text, cursor)
	if cursor == 0 {
		return text, cursor
	}
	_, size := utf8.DecodeLastRuneInString(text[:cursor])
	return text[:cursor-size] + text[cursor:], cursor - size
}

// deleteForward removes the character at the insertion point.
func deleteForward(text string, cursor int) (string, int) {
	cursor = clampCursor(text, cursor)
	if cursor >= len(text) {
		return text, cursor
	}
	_, size := utf8.DecodeRuneInString(text[cursor:])
	return text[:cursor] + text[cursor+size:], cursor
}

// deleteWordBackward removes the word before the insertion point.
func deleteWordBackward(text string, cursor int) (string, int) {
	cursor = clampCursor(text, cursor)
	trimmed := strings.TrimRight(text[:cursor], " \t")
	index := strings.LastIndexAny(trimmed, " \t")
	if index < 0 {
		return text[cursor:], 0
	}
	return text[:index] + text[cursor:], index
}

// moveCursorRune moves the insertion point by one character.
func moveCursorRune(text string, cursor int, delta int) int {
	cursor = clampCursor(text, cursor)
	if delta < 0 {
		if cursor == 0 {
			return cursor
		}
		_, size := utf8.DecodeLastRuneInString(text[:cursor])
		return cursor - size
	}
	if cursor >= len(text) {
		return cursor
	}
	_, size := utf8.DecodeRuneInString(text[cursor:])
	return cursor + size
}

// moveCursorWord moves the insertion point by one word.
func moveCursorWord(text string, cursor int, delta int) int {
	cursor = clampCursor(text, cursor)
	if delta < 0 {
		trimmed := strings.TrimRight(text[:cursor], " \t")
		index := strings.LastIndexAny(trimmed, " \t")
		if index < 0 {
			return 0
		}
		return index
	}
	for cursor < len(text) {
		rune, size := utf8.DecodeRuneInString(text[cursor:])
		if !unicode.IsSpace(rune) {
			break
		}
		cursor += size
	}
	for cursor < len(text) {
		rune, size := utf8.DecodeRuneInString(text[cursor:])
		if unicode.IsSpace(rune) {
			break
		}
		cursor += size
	}
	return cursor
}

// moveCursorLine moves the insertion point one line up or down and returns the
// new position together with a report of whether the move stayed inside the
// text.
func moveCursorLine(text string, cursor, delta int) (int, bool) {
	cursor = clampCursor(text, cursor)
	if delta == 0 {
		return cursor, true
	}
	lines := strings.Split(text, "\n")
	offsets := lineOffsets(lines)

	line := 0
	for index, offset := range offsets {
		if cursor >= offset {
			line = index
		}
	}
	column := cursor - offsets[line]
	target := line + delta
	if target < 0 || target >= len(lines) {
		return cursor, false
	}
	if column > len(lines[target]) {
		column = len(lines[target])
	}
	return offsets[target] + column, true
}

// lineOffsets returns the byte offset of every line of the text.
func lineOffsets(lines []string) []int {
	offsets := make([]int, 0, len(lines))
	offset := 0
	for _, line := range lines {
		offsets = append(offsets, offset)
		offset += len(line) + 1
	}
	return offsets
}

// moveCursorLineEdge moves the insertion point to the start or the end of its
// line.
func moveCursorLineEdge(text string, cursor int, end bool) int {
	cursor = clampCursor(text, cursor)
	lines := strings.Split(text, "\n")
	offsets := lineOffsets(lines)
	line := 0
	for index, offset := range offsets {
		if cursor >= offset {
			line = index
		}
	}
	if end {
		return offsets[line] + len(lines[line])
	}
	return offsets[line]
}

// insertCursorBlock splits the composer text at the insertion point and marks
// the insertion point with a block glyph.
func insertCursorBlock(text string, cursor int) []string {
	cursor = clampCursor(text, cursor)
	lines := strings.Split(text[:cursor]+"\x00"+text[cursor:], "\n")
	for index, line := range lines {
		lines[index] = strings.Replace(line, "\x00", "█", 1)
	}
	return lines
}
