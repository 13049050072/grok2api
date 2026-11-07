package provider

// IndexFoldASCII returns the byte offset in s of the first match of substr,
// folding ASCII letters only, or -1. Offsets found in strings.ToLower(s) must
// not be used to slice s: lowering changes the byte length of some runes
// (U+023A grows from 2 to 3 bytes, U+212A shrinks from 3 to 1).
func IndexFoldASCII(s, substr string) int {
	if substr == "" {
		return 0
	}
	lowerFirst, upperFirst := lowerASCII(substr[0]), upperASCII(substr[0])
	for index := 0; index+len(substr) <= len(s); index++ {
		if c := s[index]; (c == lowerFirst || c == upperFirst) && HasPrefixFoldASCII(s[index:], substr) {
			return index
		}
	}
	return -1
}

// HasPrefixFoldASCII reports whether s starts with prefix, folding ASCII
// letters only.
func HasPrefixFoldASCII(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for index := 0; index < len(prefix); index++ {
		if lowerASCII(s[index]) != lowerASCII(prefix[index]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

func upperASCII(c byte) byte {
	if 'a' <= c && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}
