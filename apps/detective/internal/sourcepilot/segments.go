package sourcepilot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SegmentsVersion identifies the fixed source-body segmentation rules.
const SegmentsVersion = "detective-source-segments/v1"

// Segment references an exact half-open byte range in the decoded UTF-8 body,
// not in the enclosing raw JSON. Number is one-based within that body.
type Segment struct {
	Number    int    `json:"number"`
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
	Text      string `json:"text"`
}

// SegmentedBody binds the complete ordered segment set to the unchanged body.
type SegmentedBody struct {
	Version    string    `json:"version"`
	BodySHA256 string    `json:"body_sha256"`
	Segments   []Segment `json:"segments"`
}

// SegmentBody splits at CR, LF, CRLF, and case-insensitive <br>, <br/>, <br />.
// Unicode boundary whitespace is omitted while byte offsets still refer to the
// original body. Empty pieces are skipped. Other HTML and entities stay literal.
// This is not a DOM parser or sanitizer: even a literal BR inside quoted text,
// an HTML attribute, or a script is a delimiter under these fixed rules.
func SegmentBody(body string) (SegmentedBody, error) {
	if len(body) > 32<<10 || !utf8.ValidString(body) {
		return SegmentedBody{}, errors.New("invalid segment body size or UTF-8")
	}
	digest := sha256.Sum256([]byte(body))
	result := SegmentedBody{
		Version: SegmentsVersion, BodySHA256: hex.EncodeToString(digest[:]),
		Segments: []Segment{},
	}
	appendSpan := func(start, end int) error {
		piece := body[start:end]
		leftTrimmed := strings.TrimLeftFunc(piece, unicode.IsSpace)
		start += len(piece) - len(leftTrimmed)
		text := strings.TrimRightFunc(leftTrimmed, unicode.IsSpace)
		if text == "" {
			return nil
		}
		if len(result.Segments) == 64 {
			return errors.New("body exceeds nonempty segment limit")
		}
		result.Segments = append(result.Segments, Segment{
			Number: len(result.Segments) + 1, StartByte: start, EndByte: start + len(text), Text: text,
		})
		return nil
	}
	start := 0
	for offset := 0; offset < len(body); {
		length := segmentSeparatorLength(body[offset:])
		if length == 0 {
			offset++
			continue
		}
		if err := appendSpan(start, offset); err != nil {
			return SegmentedBody{}, err
		}
		offset += length
		start = offset
	}
	if err := appendSpan(start, len(body)); err != nil {
		return SegmentedBody{}, err
	}
	return result, nil
}

// ValidateSegments requires the complete deterministic set for this exact body.
// It rejects altered hashes, text, offsets, numbering, order, or omitted segments.
func ValidateSegments(body string, set SegmentedBody) error {
	want, err := SegmentBody(body)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(set, want) {
		return errors.New("segments do not match the complete source body")
	}
	return nil
}

func segmentSeparatorLength(tail string) int {
	switch tail[0] {
	case '\r':
		if len(tail) > 1 && tail[1] == '\n' {
			return 2
		}
		return 1
	case '\n':
		return 1
	case '<':
		for _, separator := range []string{"<br>", "<br/>", "<br />"} {
			if len(tail) >= len(separator) && strings.EqualFold(tail[:len(separator)], separator) {
				return len(separator)
			}
		}
	}
	return 0
}
