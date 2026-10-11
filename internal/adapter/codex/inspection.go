package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
)

type wireRecord struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   *uint64         `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	Timestamp  string `json:"timestamp"`
	CLIVersion string `json:"cli_version"`
}

type initialInspection struct {
	recognized        bool
	valid             bool
	ordinal           bool
	possibleMalformed bool
	cliVersion        string
	sessionID         string
	startedAt         *time.Time
	diagnostics       []model.Diagnostic
}

func inspectInitial(ctx context.Context, reader *bufio.Reader, replay io.Writer) (initialInspection, error) {
	result := initialInspection{cliVersion: "unknown"}
	for recordIndex := 0; recordIndex < initialRecords; recordIndex++ {
		prefix := make([]byte, 0, inspectionPrefix)
		for {
			if err := ctx.Err(); err != nil {
				return initialInspection{}, err
			}
			fragment, err := reader.ReadSlice('\n')
			if len(fragment) > 0 {
				if replay != nil {
					if _, writeErr := replay.Write(fragment); writeErr != nil {
						return initialInspection{}, fmt.Errorf("write rollout replay file: %w", writeErr)
					}
				}
				if remaining := inspectionPrefix - len(prefix); remaining > 0 {
					prefix = append(prefix, fragment[:min(remaining, len(fragment))]...)
				}
			}
			if err == nil {
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) {
				if len(prefix) > 0 {
					result.possibleMalformed = result.possibleMalformed || looksCodexLike(prefix)
				}
				return result, nil
			}
			return initialInspection{}, err
		}
		if len(prefix) == inspectionPrefix && prefix[len(prefix)-1] != '\n' {
			result.possibleMalformed = result.possibleMalformed || looksCodexLike(prefix)
			if header, ok := boundedRecordHeader(prefix); ok {
				result.valid = true
				if knownTopLevel(header.Type) {
					result.recognized = true
				}
				if header.Ordinal != nil {
					result.ordinal = true
				}
				if result.sessionID == "" && header.Type == "session_meta" {
					if meta, ok := boundedSessionMeta(prefix); ok {
						applySessionMeta(&result, meta, header.Timestamp)
					}
				}
			}
			continue
		}
		line := trimLineEnding(prefix)
		var object map[string]json.RawMessage
		if json.Unmarshal(line, &object) != nil || object == nil {
			result.possibleMalformed = result.possibleMalformed || looksCodexLike(line)
			result.diagnostics = append(result.diagnostics, probeMalformedDiagnostic())
			continue
		}
		result.valid = true
		typeName := rawString(object["type"])
		if knownTopLevel(typeName) {
			result.recognized = true
		}
		if _, ok := validUint64(object["ordinal"]); ok {
			result.ordinal = true
		}
		if result.sessionID != "" || typeName != "session_meta" {
			continue
		}
		var payload sessionMeta
		if raw, ok := object["payload"]; !ok || json.Unmarshal(raw, &payload) != nil {
			continue
		}
		applySessionMeta(&result, payload, rawString(object["timestamp"]))
	}
	return result, nil
}

func validUint64(raw json.RawMessage) (uint64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var value uint64
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func boundedRecordHeader(prefix []byte) (wireRecord, bool) {
	payloadKey := bytes.Index(prefix, []byte(`"payload"`))
	if payloadKey < 0 {
		return wireRecord{}, false
	}
	colonOffset := bytes.IndexByte(prefix[payloadKey+len(`"payload"`):], ':')
	if colonOffset < 0 {
		return wireRecord{}, false
	}
	colon := payloadKey + len(`"payload"`) + colonOffset
	candidate := make([]byte, 0, colon+len(":null}"))
	candidate = append(candidate, prefix[:colon+1]...)
	candidate = append(candidate, []byte("null}")...)
	var header wireRecord
	if json.Unmarshal(candidate, &header) != nil {
		return wireRecord{}, false
	}
	return header, true
}

func boundedSessionMeta(prefix []byte) (sessionMeta, bool) {
	payloadKey := bytes.Index(prefix, []byte(`"payload"`))
	if payloadKey < 0 {
		return sessionMeta{}, false
	}
	payload := prefix[payloadKey+len(`"payload"`):]
	meta := sessionMeta{}
	meta.SessionID, _ = boundedStringField(payload, "session_id")
	meta.ID, _ = boundedStringField(payload, "id")
	meta.CLIVersion, _ = boundedStringField(payload, "cli_version")
	meta.Timestamp, _ = boundedStringField(payload, "timestamp")
	if strings.TrimSpace(meta.SessionID) == "" && strings.TrimSpace(meta.ID) == "" {
		return sessionMeta{}, false
	}
	return meta, true
}

func boundedStringField(data []byte, key string) (string, bool) {
	keyOffset := bytes.Index(data, []byte(`"`+key+`"`))
	if keyOffset < 0 {
		return "", false
	}
	rest := data[keyOffset+len(key)+2:]
	colon := bytes.IndexByte(rest, ':')
	if colon < 0 {
		return "", false
	}
	rest = bytes.TrimLeft(rest[colon+1:], " \t\r\n")
	if len(rest) == 0 || rest[0] != '"' {
		return "", false
	}
	escaped := false
	for index := 1; index < len(rest); index++ {
		switch {
		case escaped:
			escaped = false
		case rest[index] == '\\':
			escaped = true
		case rest[index] == '"':
			var value string
			if json.Unmarshal(rest[:index+1], &value) != nil {
				return "", false
			}
			return value, true
		}
	}
	return "", false
}

func applySessionMeta(result *initialInspection, meta sessionMeta, recordTimestamp string) {
	sessionID := strings.TrimSpace(meta.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(meta.ID)
	}
	if sessionID == "" {
		return
	}
	result.sessionID = sessionID
	if cli := strings.TrimSpace(meta.CLIVersion); cli != "" {
		result.cliVersion = cli
	}
	timestamp := strings.TrimSpace(meta.Timestamp)
	if timestamp == "" {
		timestamp = strings.TrimSpace(recordTimestamp)
	}
	if timestamp == "" {
		return
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		result.diagnostics = append(result.diagnostics, model.Diagnostic{
			Code: "codex.session.timestamp.invalid", Severity: model.SeverityWarning,
			Message: "The Codex session metadata timestamp is malformed.",
		})
		return
	}
	result.startedAt = &parsed
}

func probeMalformedDiagnostic() model.Diagnostic {
	return model.Diagnostic{
		Code: "codex.probe.malformed", Severity: model.SeverityWarning,
		Message: "A complete JSONL record in the initial inspection window is malformed.",
	}
}

func sessionInspectionDiagnostics(diagnostics []model.Diagnostic) []model.Diagnostic {
	var result []model.Diagnostic
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "codex.session.timestamp.invalid" {
			result = append(result, diagnostic)
		}
	}
	return result
}

func knownTopLevel(kind string) bool {
	switch kind {
	case "session_meta", "response_item", "event_msg", "compacted", "turn_context", "world_state", "inter_agent_communication", "inter_agent_communication_metadata":
		return true
	default:
		return false
	}
}

func looksCodexLike(line []byte) bool {
	for _, label := range []string{"session_meta", "response_item", "event_msg", "compacted", "turn_context", "world_state"} {
		if bytes.Contains(line, []byte(`"type":"`+label)) || bytes.Contains(line, []byte(`"type": "`+label)) {
			return true
		}
	}
	return false
}
