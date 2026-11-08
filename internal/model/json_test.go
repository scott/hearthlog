package model

import (
	"testing"
)

func TestValidateJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"surrogatepair", `"\ud83d\ude00"`, false},
		{"escapedliteralbackslashu", `"\\u0041"`, false},
		{"0e10", `0e10`, false},
		{"-0.0", `-0.0`, false},
		{"5e-324", `5e-324`, false},
		{"exactint64", `9223372036854775807`, false},
		{"escapedduplicatekey", `{"a":1,"\u0061":2}`, true},
		{"nestedduplicate", `{"a":{"b":1,"b":2}}`, true},
		{"rawinvalidUTF8", "\xff\xfe", true},
		{"unpairedhighsurrogate", `"\ud800"`, true},
		{"unpairedlowsurrogate", `"\udc00"`, true},
		{"truncatedsurrogate", `"\ud83d\udc`, true},
		{"trailingsecondroot", `1 2`, true},
		{"int64overflow", `9223372036854775808`, true},
		{"nonzerofloatunderflow", `1e-400`, true},
		{"floatoverflow", `1e400`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic: %v", r)
				}
			}()
			err := ValidateJSON([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateJSON(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
