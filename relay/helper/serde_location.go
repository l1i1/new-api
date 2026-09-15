package helper

import (
	"bytes"
	"io"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// The official DeepSeek endpoints reject request-shape problems with serde_json
// deserialization texts that end in a location suffix:
//
//	Failed to deserialize the JSON body into the target type: <path>: <reason> at line 1 column N
//
// N is the 1-based byte offset at which the deserializer stopped, so it is a
// pure function of the request bytes the client sent. The gateway synthesizes
// these texts locally (it never forwards the upstream body), so it can compute
// the same offset from the request it received and stay byte-identical. The
// text is body-specific, which is why the offset is derived here instead of
// being written into the message constants.
//
// Only the deserialization class carries the suffix: the body-parse class
// ("Failed to parse the request body as JSON: ...", see
// deepSeekV4ThinkingParseExpectedValueText) and the business rejections
// (invalid ranges, orphan tool messages, ...) are sent without it, matching the
// live endpoint.
type serdeLocation struct {
	body []byte
}

// newSerdeLocation wraps the raw request body. A nil receiver is valid and
// renders messages without a suffix, which is the fallback whenever the body is
// unavailable.
func newSerdeLocation(body []byte) *serdeLocation {
	if len(body) == 0 {
		return nil
	}
	return &serdeLocation{body: body}
}

// serdeLocationForRequest reads the stored request body so the official
// location suffix can be reproduced from the bytes the client actually sent.
// Any failure yields a nil location, which renders the message without the
// suffix — the same shape the gateway produced before, never a wrong column.
func serdeLocationForRequest(c *gin.Context) *serdeLocation {
	if c == nil {
		return nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil
	}
	if _, err := storage.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	body, err := io.ReadAll(storage)
	if err != nil {
		return nil
	}
	if _, err := storage.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	return newSerdeLocation(body)
}

// withLocation appends the location of the value at path to an official
// deserialization text.
func (l *serdeLocation) withLocation(message string, path ...any) string {
	if l == nil {
		return message
	}
	valueEnd, ok := locateJSONValue(l.body, path)
	if !ok {
		return message
	}
	return message + serdeColumnSuffix(valueEnd)
}

// withContainerLocation appends the location serde reports for a missing
// member. The member being absent, the reported offset is the end of the
// innermost *named* container in the path: `thinking` reports the end of the
// thinking object, `messages[2]` reports the end of the messages array. Trailing
// array positions are therefore dropped before resolving.
func (l *serdeLocation) withContainerLocation(message string, path ...any) string {
	if l == nil {
		return message
	}
	named := path
	for len(named) > 0 {
		if _, isIndex := named[len(named)-1].(int); !isIndex {
			break
		}
		named = named[:len(named)-1]
	}
	valueEnd, ok := locateJSONValue(l.body, named)
	if !ok {
		return message
	}
	return message + serdeColumnSuffix(valueEnd)
}

func serdeColumnSuffix(offset int) string {
	if offset <= 0 {
		return ""
	}
	return " at line 1 column " + strconv.Itoa(offset)
}

// locateJSONValue resolves path — object keys as string, array positions as int
// — and returns the offset just past the resolved value.
func locateJSONValue(body []byte, path []any) (int, bool) {
	if len(path) == 0 {
		return 0, false
	}
	return locateFrom(body, skipJSONSpace(body, 0), path)
}

func locateFrom(body []byte, pos int, path []any) (int, bool) {
	pos = skipJSONSpace(body, pos)
	if pos >= len(body) {
		return 0, false
	}
	switch body[pos] {
	case '{':
		return locateInObject(body, pos, path)
	case '[':
		return locateInArray(body, pos, path)
	default:
		return 0, false
	}
}

func locateInObject(body []byte, pos int, path []any) (int, bool) {
	key, ok := path[0].(string)
	if !ok {
		return 0, false
	}
	rest := path[1:]
	i := skipJSONSpace(body, pos+1)
	for i < len(body) && body[i] != '}' {
		keyEnd, ok := skipJSONString(body, i)
		if !ok {
			return 0, false
		}
		valuePos, ok := afterJSONColon(body, keyEnd)
		if !ok {
			return 0, false
		}
		if jsonKeyEquals(body, i, key) {
			if len(rest) == 0 {
				return skipJSONValue(body, valuePos)
			}
			return locateFrom(body, valuePos, rest)
		}
		valueEnd, ok := skipJSONValue(body, valuePos)
		if !ok {
			return 0, false
		}
		i = skipJSONSeparator(body, valueEnd)
	}
	return 0, false
}

func locateInArray(body []byte, pos int, path []any) (int, bool) {
	index, ok := path[0].(int)
	if !ok || index < 0 {
		return 0, false
	}
	rest := path[1:]
	i := skipJSONSpace(body, pos+1)
	for element := 0; i < len(body) && body[i] != ']'; element++ {
		if element == index {
			if len(rest) == 0 {
				return skipJSONValue(body, i)
			}
			return locateFrom(body, i, rest)
		}
		valueEnd, ok := skipJSONValue(body, i)
		if !ok {
			return 0, false
		}
		i = skipJSONSeparator(body, valueEnd)
	}
	return 0, false
}

// afterJSONColon consumes the colon that must follow an object key.
func afterJSONColon(body []byte, keyEnd int) (int, bool) {
	i := skipJSONSpace(body, keyEnd)
	if i >= len(body) || body[i] != ':' {
		return 0, false
	}
	return skipJSONSpace(body, i+1), true
}

// skipJSONSeparator consumes the comma between container members.
func skipJSONSeparator(body []byte, pos int) int {
	i := skipJSONSpace(body, pos)
	if i < len(body) && body[i] == ',' {
		return skipJSONSpace(body, i+1)
	}
	return i
}

// jsonKeyEquals reports whether the string token starting at pos is exactly key.
// Escaped keys are treated as non-matching: the field paths the official text
// names are always plain ASCII, and a miss only drops the location suffix.
func jsonKeyEquals(body []byte, pos int, key string) bool {
	end, ok := skipJSONString(body, pos)
	if !ok {
		return false
	}
	inner := body[pos+1 : end-1]
	if bytes.IndexByte(inner, '\\') >= 0 || len(inner) != len(key) {
		return false
	}
	return string(inner) == key
}

func skipJSONSpace(body []byte, pos int) int {
	for pos < len(body) {
		switch body[pos] {
		case ' ', '\t', '\n', '\r':
			pos++
		default:
			return pos
		}
	}
	return pos
}

// skipJSONString returns the offset just past the closing quote of the string
// that starts at pos.
func skipJSONString(body []byte, pos int) (int, bool) {
	if pos >= len(body) || body[pos] != '"' {
		return 0, false
	}
	for i := pos + 1; i < len(body); i++ {
		switch body[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

// skipJSONValue returns the offset just past the complete JSON value at pos.
func skipJSONValue(body []byte, pos int) (int, bool) {
	pos = skipJSONSpace(body, pos)
	if pos >= len(body) {
		return 0, false
	}
	switch body[pos] {
	case '"':
		return skipJSONString(body, pos)
	case '{', '[':
		// JSON is properly nested, so counting only this container's own
		// delimiter pair is enough; strings are skipped wholesale.
		open := body[pos]
		closing := byte('}')
		if open == '[' {
			closing = ']'
		}
		depth := 0
		for i := pos; i < len(body); i++ {
			switch body[i] {
			case '"':
				end, ok := skipJSONString(body, i)
				if !ok {
					return 0, false
				}
				i = end - 1
			case open:
				depth++
			case closing:
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
		}
		return 0, false
	default:
		i := pos
		for i < len(body) && !isJSONDelimiter(body[i]) {
			i++
		}
		if i == pos {
			return 0, false
		}
		return i, true
	}
}

func isJSONDelimiter(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', ',', '}', ']':
		return true
	}
	return false
}
