package irc

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// ircMessage is one parsed IRC line. Only the fields this product needs are
// extracted: prefix, command, middle params, and the trailing parameter.
type ircMessage struct {
	prefix   string
	command  string
	params   []string
	trailing string
}

// parseLine parses a single IRC protocol line. The format is:
//
//	[:prefix SPACE] command [params...] [":" trailing]
//
// Parsing is deliberately minimal. It covers the subset the real client
// consumes and nothing more.
func parseLine(line string) ircMessage {
	var msg ircMessage

	if strings.HasPrefix(line, ":") {
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			msg.prefix = line[1:]
			return msg
		}
		msg.prefix = line[1:sp]
		line = line[sp+1:]
	}

	// The trailing parameter is everything after the first " :".
	if idx := strings.Index(line, " :"); idx >= 0 {
		msg.trailing = line[idx+2:]
		line = line[:idx]
	}

	fields := strings.Fields(line)
	if len(fields) > 0 {
		msg.command = fields[0]
		msg.params = fields[1:]
	}
	return msg
}

var (
	eventSeqMu sync.Mutex
	eventSeq   int
)

func newEventID() string {
	eventSeqMu.Lock()
	eventSeq++
	n := eventSeq
	eventSeqMu.Unlock()
	return "evt-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(n)
}

// nowUnix returns a nanosecond timestamp. The bridge uses this local arrival
// timestamp as a replay watermark; second precision can make distinct IRC
// messages received in the same second look like history.
func nowUnix() int64 { return time.Now().UnixNano() }
