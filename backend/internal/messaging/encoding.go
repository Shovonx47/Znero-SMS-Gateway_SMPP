package messaging

import (
	"fmt"
	"math/rand"
	"unicode/utf16"
)

// GSM 7-bit default alphabet lookup table
var gsm7Basic = map[rune]bool{
	'@': true, '£': true, '$': true, '¥': true, 'è': true, 'é': true, 'ù': true, 'ì': true, 'ò': true, 'Ç': true,
	'\n': true, 'Ø': true, 'ø': true, '\r': true, 'Å': true, 'å': true, 'Δ': true, '_': true, 'Φ': true, 'Γ': true,
	'Λ': true, 'Ω': true, 'Π': true, 'Ψ': true, 'Σ': true, 'Θ': true, 'Ξ': true, 'Æ': true, 'æ': true, 'ß': true,
	'É': true, ' ': true, '!': true, '"': true, '#': true, '¤': true, '%': true, '&': true, '\'': true, '(': true,
	')': true, '*': true, '+': true, ',': true, '-': true, '.': true, '/': true, '0': true, '1': true, '2': true,
	'3': true, '4': true, '5': true, '6': true, '7': true, '8': true, '9': true, ':': true, ';': true, '<': true,
	'=': true, '>': true, '?': true, '¡': true, 'A': true, 'B': true, 'C': true, 'D': true, 'E': true, 'F': true,
	'G': true, 'H': true, 'I': true, 'J': true, 'K': true, 'L': true, 'M': true, 'N': true, 'O': true, 'P': true,
	'Q': true, 'R': true, 'S': true, 'T': true, 'U': true, 'V': true, 'W': true, 'X': true, 'Y': true, 'Z': true,
	'Ä': true, 'Ö': true, 'Ñ': true, 'Ü': true, '§': true, '¿': true, 'a': true, 'b': true, 'c': true, 'd': true,
	'e': true, 'f': true, 'g': true, 'h': true, 'i': true, 'j': true, 'k': true, 'l': true, 'm': true, 'n': true,
	'o': true, 'p': true, 'q': true, 'r': true, 's': true, 't': true, 'u': true, 'v': true, 'w': true, 'x': true,
	'y': true, 'z': true, 'ä': true, 'ö': true, 'ñ': true, 'ü': true, 'à': true,
}

// GSM 7-bit extended alphabet (escape table, counts as 2 septets)
var gsm7Extension = map[rune]bool{
	'\f': true, '^': true, '{': true, '}': true, '\\': true, '[': true, '~': true, ']': true, '|': true, '€': true,
}

const (
	EncodingGSM7 = "GSM7"
	EncodingUCS2 = "UCS2"

	GSM7SingleMax = 160
	GSM7ConcatMax = 153

	UCS2SingleMax = 70
	UCS2ConcatMax = 67
)

// DetectEncoding examines the text to determine whether it can be encoded in GSM-7 or requires UCS-2.
func DetectEncoding(text string) (string, int) {
	isGSM := true
	gsmLength := 0

	for _, r := range text {
		if gsm7Basic[r] {
			gsmLength++
		} else if gsm7Extension[r] {
			gsmLength += 2 // Escaped character costs 2 septets
		} else {
			isGSM = false
			break
		}
	}

	if isGSM {
		return EncodingGSM7, gsmLength
	}

	// For UCS-2, calculate UTF-16 code units
	utf16Units := len(utf16.Encode([]rune(text)))
	return EncodingUCS2, utf16Units
}

// CalculateSegments computes the encoding, billable segments, and length, checking against maxSegments.
func CalculateSegments(text string, maxSegments int) (string, int, int, error) {
	if len(text) == 0 {
		return "", 0, 0, fmt.Errorf("message text cannot be empty")
	}

	encoding, unitCount := DetectEncoding(text)
	var parts int

	if encoding == EncodingGSM7 {
		if unitCount <= GSM7SingleMax {
			parts = 1
		} else {
			parts = (unitCount + GSM7ConcatMax - 1) / GSM7ConcatMax
		}
	} else {
		if unitCount <= UCS2SingleMax {
			parts = 1
		} else {
			parts = (unitCount + UCS2ConcatMax - 1) / UCS2ConcatMax
		}
	}

	if maxSegments > 0 && parts > maxSegments {
		return encoding, parts, unitCount, fmt.Errorf("%w: %d parts calculated, maximum allowed is %d", ErrMaxSegmentsExceeded, parts, maxSegments)
	}

	return encoding, parts, unitCount, nil
}

// SplitMessageParts splits the input text into segment strings and generates 6-byte GSM 03.40 UDH headers.
func SplitMessageParts(text string, encoding string) ([]string, [][]byte) {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil, nil
	}

	_, totalParts, _, _ := CalculateSegments(text, 100)
	if totalParts <= 1 {
		return []string{text}, [][]byte{nil}
	}

	var partBodies []string
	var udhs [][]byte
	refNum := byte(rand.Intn(256))

	chunkSize := GSM7ConcatMax
	if encoding == EncodingUCS2 {
		chunkSize = UCS2ConcatMax
	}

	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		partBodies = append(partBodies, string(runes[i:end]))
	}

	actualTotal := len(partBodies)
	for idx := 1; idx <= actualTotal; idx++ {
		// GSM 03.40 6-byte concatenated SMS UDH:
		// [0x05 (UDHL), 0x00 (IEI for 8-bit concat), 0x03 (IEDL), refNum, totalParts, seqNum]
		udh := []byte{0x05, 0x00, 0x03, refNum, byte(actualTotal), byte(idx)}
		udhs = append(udhs, udh)
	}

	return partBodies, udhs
}

