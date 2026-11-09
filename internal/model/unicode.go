package model

import (
	"unicode/utf8"
)

// validateUnicode rejects malformed UTF-8 and unpaired raw JSON surrogate escapes.
func validateUnicode(data []byte) error {
	if !utf8.Valid(data) {
		return ErrInvalidJSON
	}
	i := 0
	for i < len(data) {
		if data[i] != '"' {
			i++
			continue
		}
		i++ // skip opening quote
		for i < len(data) {
			c := data[i]
			if c == '\\' {
				if i+1 >= len(data) {
					return ErrInvalidJSON
				}
				if data[i+1] == 'u' {
					if i+5 >= len(data) {
						return ErrInvalidJSON
					} else {
						hex := data[i+2 : i+6]
						v, ok := parseHex4(hex)
						if !ok {
							return ErrInvalidJSON
						}
						// Check for unpaired surrogate escape
						if v >= 0xD800 && v <= 0xDBFF {
							// Must be followed by \uXXXX low surrogate
							if i+11 < len(data) && data[i+6] == '\\' && data[i+7] == 'u' {
								hex2 := data[i+8 : i+12]
								v2, ok2 := parseHex4(hex2)
								if !ok2 || v2 < 0xDC00 || v2 > 0xDFFF {
									return ErrInvalidJSON
								}
								i += 12
								continue
							} else {
								return ErrInvalidJSON
							}
						} else if v >= 0xDC00 && v <= 0xDFFF {
							return ErrInvalidJSON
						}
					}
					i += 6
					continue
				}
				i += 2
				continue
			}
			if c == '"' {
				break
			}
			i++
		}
		i++ // skip closing quote
	}
	return nil
}

func parseHex4(b []byte) (int, bool) {
	if len(b) != 4 {
		return 0, false
	}
	var v int
	for _, c := range b {
		d := -1
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, false
		}
		v = v<<4 | d
	}
	return v, true
}
