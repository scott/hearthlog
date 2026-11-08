package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestValidateAttributesBounds(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"empty_object", `{}`, false},
		{"depth8_scalar", depthFixture(8, false), false},
		{"depth8_empty", depthFixture(8, true), false},
		{"depth9_scalar", depthFixture(9, false), true},
		{"depth9_empty", depthFixture(9, true), true},
		{"flat_128_members", flatMembers(128), false},
		{"flat_129_members", flatMembers(129), true},
		{"nested_total_128", nestedTotal(128), false},
		{"nested_total_129", nestedTotal(129), true},
		{"array_128", `{"a":` + buildArray(128) + `}`, false},
		{"array_129", `{"a":` + buildArray(129) + `}`, true},
		{"key_128_bytes", `{"` + strings.Repeat("k", 128) + `":1}`, false},
		{"key_129_bytes", `{"` + strings.Repeat("k", 129) + `":1}`, true},
		{"string_8192", `{"a":"` + strings.Repeat("x", 8192) + `"}`, false},
		{"string_8193", `{"a":"` + strings.Repeat("x", 8193) + `"}`, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !json.Valid([]byte(tc.raw)) {
				t.Fatalf("fixture not valid JSON: %s", tc.name)
			}
			err := ValidateAttributes(json.RawMessage(tc.raw))
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func depthFixture(n int, empty bool) string {
	var v any
	if empty {
		v = map[string]any{}
	} else {
		v = 1
	}
	wraps := n
	if empty {
		wraps = n - 1
	}
	for i := 0; i < wraps; i++ {
		v = map[string]any{"a": v}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func flatMembers(n int) string {
	var b strings.Builder
	b.WriteString(`{`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(`,`)
		}
		fmt.Fprintf(&b, `"k%d":1`, i)
	}
	b.WriteString(`}`)
	return b.String()
}

func nestedTotal(n int) string {
	inner := n - 1
	var b strings.Builder
	b.WriteString(`{"root":{`)
	for i := 0; i < inner; i++ {
		if i > 0 {
			b.WriteString(`,`)
		}
		fmt.Fprintf(&b, `"k%d":1`, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

func buildArray(n int) string {
	var b strings.Builder
	b.WriteString(`[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(`,`)
		}
		b.WriteString(`1`)
	}
	b.WriteString(`]`)
	return b.String()
}
