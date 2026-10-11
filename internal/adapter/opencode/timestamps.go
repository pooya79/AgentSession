package opencode

import (
	"math"
	"strconv"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
)

func millisecondTime(value any, code string) (*time.Time, *model.Diagnostic) {
	var milliseconds int64
	switch value := value.(type) {
	case int64:
		milliseconds = value
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || value > math.MaxInt64 || value < math.MinInt64 {
			return nil, timestampDiagnostic(code)
		}
		milliseconds = int64(value)
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, timestampDiagnostic(code)
		}
		milliseconds = parsed
	case []byte:
		parsed, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return nil, timestampDiagnostic(code)
		}
		milliseconds = parsed
	case nil:
		return nil, nil
	default:
		return nil, timestampDiagnostic(code)
	}
	valueTime := time.UnixMilli(milliseconds).UTC()
	if valueTime.Year() < 0 || valueTime.Year() > 9999 {
		return nil, timestampDiagnostic(code)
	}
	return &valueTime, nil
}

func timestampDiagnostic(code string) *model.Diagnostic {
	return &model.Diagnostic{Code: code, Severity: model.SeverityWarning, Message: "An OpenCode millisecond timestamp is malformed; source order was preserved."}
}
