package irc

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// maxIRCBody is the largest PRIVMSG trailing we will send. IRC lines are
// capped at 512 bytes including prefix, command, and CRLF.
const maxIRCBody = 350

const chunkPrefix = "jkp+ "

// chunkTTL bounds how long an incomplete multi-chunk buffer stays around.
const chunkTTL = 2 * time.Minute

var chunkSequence atomic.Uint32

// splitBody flattens line breaks and splits long messages on UTF-8 rune
// boundaries. Chunk identifiers prevent fragments from separate messages from
// being assembled together.
func splitBody(body string) []string {
	body = strings.ToValidUTF8(body, "\uFFFD")
	body = strings.ReplaceAll(body, "\r\n", " ")
	body = strings.ReplaceAll(body, "\r", " ")
	body = strings.ReplaceAll(body, "\n", " ")
	if len(body) <= maxIRCBody {
		return []string{body}
	}

	id := fmt.Sprintf("%08x", chunkSequence.Add(1))
	maxPayload := maxIRCBody - len("jkp+ ffffffff 99/99 ")
	chunks := sliceUTF8(body, maxPayload)
	parts := make([]string, 0, len(chunks))
	for i, payload := range chunks {
		parts = append(parts, fmt.Sprintf("%s%s %d/%d %s", chunkPrefix, id, i+1, len(chunks), payload))
	}
	return parts
}

func sliceUTF8(body string, maxBytes int) []string {
	var chunks []string
	for start := 0; start < len(body); {
		end := start + maxBytes
		if end >= len(body) {
			chunks = append(chunks, body[start:])
			break
		}
		for end > start && !utf8.RuneStart(body[end]) {
			end--
		}
		if end == start {
			_, size := utf8.DecodeRuneInString(body[start:])
			end += size
		}
		chunks = append(chunks, body[start:end])
		start = end
	}
	return chunks
}

func parseChunk(body string) (index, total int, payload string, ok bool) {
	_, index, total, payload, ok = parseChunkWithID(body)
	return index, total, payload, ok
}

// parseChunkWithID parses both the current ID-tagged frame and the legacy
// frame, which had no message identifier.
func parseChunkWithID(body string) (id string, index, total int, payload string, ok bool) {
	if !strings.HasPrefix(body, chunkPrefix) {
		return "", 0, 0, "", false
	}
	rest := strings.TrimPrefix(body, chunkPrefix)
	space := strings.IndexByte(rest, ' ')
	if space <= 0 {
		return "", 0, 0, "", false
	}
	first, remaining := rest[:space], rest[space+1:]
	if index, total, valid := parseFraction(first); valid {
		return "", index, total, remaining, true
	}
	space = strings.IndexByte(remaining, ' ')
	if space <= 0 {
		return "", 0, 0, "", false
	}
	index, total, valid := parseFraction(remaining[:space])
	if !valid {
		return "", 0, 0, "", false
	}
	return first, index, total, remaining[space+1:], true
}

func parseFraction(frac string) (index, total int, ok bool) {
	slash := strings.IndexByte(frac, '/')
	if slash <= 0 {
		return 0, 0, false
	}
	index, err1 := strconv.Atoi(frac[:slash])
	total, err2 := strconv.Atoi(frac[slash+1:])
	if err1 != nil || err2 != nil || index < 1 || total < 1 || index > total {
		return 0, 0, false
	}
	return index, total, true
}

type chunkAssembler struct {
	id        string
	total     int
	parts     []string
	have      int
	updatedAt time.Time
}

func (a *chunkAssembler) reset(id string, total int) {
	a.id = id
	a.total = total
	a.parts = make([]string, total)
	a.have = 0
}

func (a *chunkAssembler) add(id string, index, total int, payload string, now time.Time) (string, bool) {
	if a.total != 0 && !a.updatedAt.IsZero() && now.Sub(a.updatedAt) > chunkTTL {
		*a = chunkAssembler{}
	}
	if a.total == 0 || a.total != total || a.id != id || (index == 1 && a.parts[0] != "") {
		a.reset(id, total)
	}
	if index > len(a.parts) || a.parts[index-1] != "" {
		return "", false
	}
	a.parts[index-1] = payload
	a.have++
	a.updatedAt = now
	if a.have < a.total {
		return "", false
	}
	body := strings.Join(a.parts, "")
	*a = chunkAssembler{}
	return body, true
}
