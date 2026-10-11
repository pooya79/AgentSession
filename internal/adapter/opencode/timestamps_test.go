package opencode

import (
	"math"
	"testing"
	"time"
)

func TestMillisecondTimeRejectsValuesOutsideRFC3339Range(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		valid bool
	}{
		{name: "ordinary", value: 1700000000000, valid: true},
		{name: "year zero", value: time.Date(0, time.January, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), valid: true},
		{name: "last millisecond of year 9999", value: time.Date(9999, time.December, 31, 23, 59, 59, int(time.Millisecond*999), time.UTC).UnixMilli(), valid: true},
		{name: "year 10000", value: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC).UnixMilli()},
		{name: "maximum integer", value: math.MaxInt64},
		{name: "minimum integer", value: math.MinInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, diagnostic := millisecondTime(test.value, "timestamp.invalid")
			if test.valid && (value == nil || diagnostic != nil) {
				t.Fatalf("valid timestamp = %v, %#v", value, diagnostic)
			}
			if !test.valid && (value != nil || diagnostic == nil || diagnostic.Code != "timestamp.invalid") {
				t.Fatalf("invalid timestamp = %v, %#v", value, diagnostic)
			}
		})
	}
}
