package table

import "unicode"

// DisplayWidth returns the number of terminal cells the string occupies.
// Combining marks and format characters take zero cells, East Asian wide
// characters and most emoji take two, everything else takes one.
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 0x300:
		return 1
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r), unicode.Is(unicode.Cf, r):
		return 0
	case isWide(r):
		return 2
	}
	return 1
}

var wideRanges = [][2]rune{
	{0x1100, 0x115F},   // Hangul Jamo
	{0x2E80, 0x303E},   // CJK radicals, punctuation
	{0x3041, 0x33FF},   // Hiragana, Katakana, CJK symbols
	{0x3400, 0x4DBF},   // CJK Ext A
	{0x4E00, 0x9FFF},   // CJK Unified
	{0xA000, 0xA4CF},   // Yi
	{0xAC00, 0xD7A3},   // Hangul syllables
	{0xF900, 0xFAFF},   // CJK compatibility
	{0xFE30, 0xFE4F},   // CJK compatibility forms
	{0xFF00, 0xFF60},   // Fullwidth forms
	{0xFFE0, 0xFFE6},   // Fullwidth signs
	{0x1F300, 0x1F64F}, // Misc symbols & pictographs, emoticons
	{0x1F680, 0x1F6FF}, // Transport & map symbols
	{0x1F900, 0x1F9FF}, // Supplemental symbols & pictographs
	{0x1FA70, 0x1FAFF}, // Symbols & pictographs extended-A
	{0x20000, 0x3FFFD}, // CJK Ext B..
}

func isWide(r rune) bool {
	for _, rg := range wideRanges {
		if r < rg[0] {
			return false
		}
		if r <= rg[1] {
			return true
		}
	}
	return false
}
