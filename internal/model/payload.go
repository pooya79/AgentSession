package model

import "fmt"

// NormalizedData is implemented only by source-neutral payloads in this
// package. Adapters map source records into these types rather than extending
// the canonical model with source-specific structures.
type NormalizedData interface {
	eventKind() EventKind
}

// MessageRole identifies a normalized participant role. Unknown represents
// missing or unrecognized evidence rather than a new source-specific role.
type MessageRole string

const (
	MessageRoleUnknown   MessageRole = "unknown"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleSystem    MessageRole = "system"
	MessageRoleOther     MessageRole = "other"
)

// MessageData contains normalized message content.
type MessageData struct {
	Role MessageRole
	Text string
}

func (MessageData) eventKind() EventKind { return EventKindMessage }

// ToolCallData describes a requested tool invocation. Input is a normalized
// textual representation and is not required to contain valid JSON.
type ToolCallData struct {
	CallID   string
	ToolName string
	Input    string
}

func (ToolCallData) eventKind() EventKind { return EventKindToolCall }

// ToolResultData describes recorded output for a tool invocation. IsError is
// nil when the source does not establish whether the result is an error.
type ToolResultData struct {
	CallID   string
	ToolName string
	Output   string
	IsError  *bool
}

func (ToolResultData) eventKind() EventKind { return EventKindToolResult }

// CommandData describes a command execution. ExitCode is nil when no exit
// status was recorded. Output preserves recorded textual output order and
// makes no claim that stdout and stderr were separately available.
type CommandData struct {
	Command          string
	WorkingDirectory string
	ExitCode         *int
	Output           string
}

func (CommandData) eventKind() EventKind { return EventKindCommand }

// FileReadData describes observed file access. Line numbers are optional,
// one-based, and inclusive when present.
type FileReadData struct {
	Path      string
	StartLine *int64
	EndLine   *int64
}

func (FileReadData) eventKind() EventKind { return EventKindFileRead }

// FileMutationOperation is the normalized effect of a file mutation.
type FileMutationOperation string

const (
	FileMutationUnknown FileMutationOperation = "unknown"
	FileMutationCreate  FileMutationOperation = "create"
	FileMutationUpdate  FileMutationOperation = "update"
	FileMutationDelete  FileMutationOperation = "delete"
	FileMutationRename  FileMutationOperation = "rename"
)

// FileMutationData describes a file creation, update, deletion, or rename.
// PreviousPath is meaningful for a rename and empty when unavailable.
type FileMutationData struct {
	Path         string
	Operation    FileMutationOperation
	PreviousPath string
}

func (FileMutationData) eventKind() EventKind { return EventKindFileMutation }

// PatchData contains recorded textual patch evidence and the affected paths
// known from normalization. Text is not required to use a particular diff
// syntax because some sources expose only partial patch representations.
type PatchData struct {
	Text  string
	Paths []string
}

func (PatchData) eventKind() EventKind { return EventKindPatch }

// UsageData contains optional counters reported by the source. A nil counter
// means unreported; a non-nil zero is a known zero value.
type UsageData struct {
	InputTokens      *int64
	OutputTokens     *int64
	CacheReadTokens  *int64
	CacheWriteTokens *int64
}

func (UsageData) eventKind() EventKind { return EventKindUsage }

// ErrorData contains a normalized error message and an optional stable code.
type ErrorData struct {
	Code    string
	Message string
}

func (ErrorData) eventKind() EventKind { return EventKindError }

// SummaryData contains a source-recorded summary rather than a derived
// analysis finding or outcome.
type SummaryData struct {
	Text string
}

func (SummaryData) eventKind() EventKind { return EventKindSummary }

// UnknownData preserves the source's record label when one is available. The
// full unsupported record remains accessible through Event.RawRecord.
type UnknownData struct {
	OriginalKind string
}

func (UnknownData) eventKind() EventKind { return EventKindUnknown }

func validateNormalizedData(data NormalizedData) error {
	switch value := data.(type) {
	case MessageData:
		if !validMessageRole(value.Role) {
			return fmt.Errorf("unsupported message role %q", value.Role)
		}
	case FileReadData:
		if value.StartLine != nil && *value.StartLine <= 0 {
			return fmt.Errorf("file read start line must be positive")
		}
		if value.EndLine != nil && *value.EndLine <= 0 {
			return fmt.Errorf("file read end line must be positive")
		}
		if value.StartLine != nil && value.EndLine != nil && *value.EndLine < *value.StartLine {
			return fmt.Errorf("file read end line must not precede start line")
		}
	case FileMutationData:
		if !validFileMutation(value.Operation) {
			return fmt.Errorf("unsupported file mutation operation %q", value.Operation)
		}
	case UsageData:
		counts := []*int64{value.InputTokens, value.OutputTokens, value.CacheReadTokens, value.CacheWriteTokens}
		for _, count := range counts {
			if count != nil && *count < 0 {
				return fmt.Errorf("usage token counts must not be negative")
			}
		}
	case ToolCallData, ToolResultData, CommandData, PatchData, ErrorData, SummaryData, UnknownData:
		// Empty fields represent incomplete evidence and remain valid.
	default:
		return fmt.Errorf("unsupported normalized data type %T", data)
	}
	return nil
}

func validMessageRole(role MessageRole) bool {
	switch role {
	case MessageRoleUnknown, MessageRoleUser, MessageRoleAssistant, MessageRoleSystem, MessageRoleOther:
		return true
	default:
		return false
	}
}

func validFileMutation(operation FileMutationOperation) bool {
	switch operation {
	case FileMutationUnknown, FileMutationCreate, FileMutationUpdate, FileMutationDelete, FileMutationRename:
		return true
	default:
		return false
	}
}
