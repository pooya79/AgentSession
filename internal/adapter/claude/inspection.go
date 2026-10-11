package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func (p *prepared) inspectRecord(line []byte, header probeRecordHeader, truncated bool) {
	var record wireRecord
	if !truncated {
		if json.Unmarshal(trimLineEnding(line), &record) != nil {
			return
		}
		if p.sessionID == "" {
			p.sessionID = strings.TrimSpace(record.SessionID)
		}
		if p.agentID == "" {
			p.agentID = strings.TrimSpace(record.AgentID)
		}
		if value := strings.TrimSpace(record.Version); value != "" {
			p.cliVersion = value
		}
		return
	}
	if p.sessionID == "" {
		p.sessionID = strings.TrimSpace(header.sessionID)
	}
	if p.agentID == "" {
		p.agentID = strings.TrimSpace(header.agentID)
	}
	if value := strings.TrimSpace(header.version); value != "" {
		p.cliVersion = value
	}
}

// replayLine copies a record to replay storage as it is read and retains only a
// bounded prefix for header inspection. The full record is reconstructed from
// replay storage by the normal import path instead of being held by Prepare.
func replayLine(reader *bufio.Reader, replay io.Writer, captureLimit int) ([]byte, probeRecordHeader, bool, bool, error) {
	sample := make([]byte, 0, min(captureLimit, readBuffer))
	inspector := probeHeaderInspector{expectKey: false}
	truncated := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if _, writeErr := replay.Write(fragment); writeErr != nil {
				return nil, probeRecordHeader{}, false, truncated, fmt.Errorf("spool Claude probe record: %w", writeErr)
			}
			inspector.write(fragment)
			remaining := captureLimit - len(sample)
			captured := 0
			if remaining > 0 {
				captured = len(fragment)
				if captured > remaining {
					captured = remaining
				}
				sample = append(sample, fragment[:captured]...)
			}
			truncated = truncated || captured < len(fragment)
		}
		if err == nil {
			return sample, inspector.header, true, truncated, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return sample, inspector.header, false, truncated, io.EOF
		}
		return sample, inspector.header, false, truncated, err
	}
}

type probeRecordHeader struct {
	sessionID string
	agentID   string
	version   string
}

type probeHeaderInspector struct {
	header    probeRecordHeader
	depth     int
	inString  bool
	escaped   bool
	expectKey bool
	pending   string
	capture   string
	value     []byte
	overflow  bool
}

func (p *probeHeaderInspector) write(data []byte) {
	const maxHeaderValue = 4 << 10
	for _, b := range data {
		if p.inString {
			if p.capture != "" && !p.overflow {
				if len(p.value) == maxHeaderValue {
					p.overflow = true
				} else {
					p.value = append(p.value, b)
				}
			}
			if p.escaped {
				p.escaped = false
				continue
			}
			if b == '\\' {
				p.escaped = true
				continue
			}
			if b == '"' {
				p.finishString()
			}
			continue
		}

		switch b {
		case '{':
			p.depth++
			if p.depth == 1 {
				p.expectKey = true
			} else if p.depth == 2 {
				p.pending = ""
			}
		case '[':
			p.depth++
			if p.depth == 2 {
				p.pending = ""
			}
		case '}', ']':
			p.depth--
		case '"':
			p.inString = true
			p.overflow = false
			p.value = p.value[:0]
			switch {
			case p.depth == 1 && p.expectKey:
				p.capture = "key"
			case p.depth == 1 && p.pending != "":
				p.capture = p.pending
			default:
				p.capture = ""
			}
			if p.capture != "" {
				p.value = append(p.value, '"')
			}
		case ',':
			if p.depth == 1 {
				p.expectKey = true
				p.pending = ""
			}
		case ' ', '\t', '\r', '\n', ':':
		default:
			if p.depth == 1 && p.pending != "" {
				p.pending = ""
			}
		}
	}
}

func (p *probeHeaderInspector) finishString() {
	p.inString = false
	if p.capture == "" || p.overflow {
		p.capture = ""
		return
	}
	var value string
	if json.Unmarshal(p.value, &value) != nil {
		p.capture = ""
		return
	}
	switch p.capture {
	case "key":
		p.expectKey = false
		if value == "sessionId" || value == "agentId" || value == "version" {
			p.pending = value
		} else {
			p.pending = ""
		}
	case "sessionId":
		p.header.sessionID = value
		p.pending = ""
	case "agentId":
		p.header.agentID = value
		p.pending = ""
	case "version":
		p.header.version = value
		p.pending = ""
	}
	p.capture = ""
}

// knownTopLevel reports whether a type belongs to the observed Claude record
// envelope vocabulary, including metadata-only records.
func knownTopLevel(kind string) bool {
	switch kind {
	case "user", "assistant", "system", "summary", "file-history-snapshot", "progress", "queue-operation",
		"attachment", "turn_duration", "status", "rate_limit_event", "last-prompt", "agent-name", "custom-title",
		"context", "context-update":
		return true
	default:
		return false
	}
}

func hasJSONField(data []byte, name string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	_, ok := fields[name]
	return ok
}

// looksClaudeLike performs a bounded textual probe for Claude envelope markers
// before full record decoding.
func looksClaudeLike(line []byte) bool {
	for _, label := range []string{
		"user", "assistant", "system", "summary", "file-history-snapshot", "progress",
		"queue-operation", "attachment", "turn_duration", "status", "rate_limit_event",
		"last-prompt", "agent-name", "custom-title", "context", "context-update",
	} {
		if bytes.Contains(line, []byte(`"type":"`+label)) || bytes.Contains(line, []byte(`"type": "`+label)) {
			return true
		}
	}
	return bytes.Contains(line, []byte(`"sessionId"`)) || bytes.Contains(line, []byte(`"agentId"`)) ||
		bytes.Contains(line, []byte(`"isSidechain"`))
}
