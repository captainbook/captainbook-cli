package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFormat_JSON(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		wantSubs []string
		wantErr  bool
	}{
		{
			name:     "pretty prints object",
			data:     `{"meta":{"period":{"from":"2025-01-01"}},"data":{"revenue":1000}}`,
			wantSubs: []string{"revenue", "1000", "  "},
		},
		{
			name:     "pretty prints array data",
			data:     `{"data":[{"name":"a"},{"name":"b"}]}`,
			wantSubs: []string{"name", `"a"`, `"b"`},
		},
		{
			name:    "invalid JSON returns error",
			data:    `not json at all`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := Format(&buf, []byte(tt.data), "json")

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Format() error: %v", err)
			}

			output := buf.String()
			// Verify it's valid JSON
			var raw json.RawMessage
			if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &raw); err != nil {
				t.Errorf("output is not valid JSON: %v\noutput: %s", err, output)
			}

			for _, sub := range tt.wantSubs {
				if !strings.Contains(output, sub) {
					t.Errorf("output missing %q\noutput: %s", sub, output)
				}
			}
		})
	}
}

func TestFormat_Table_Array(t *testing.T) {
	data := `{"meta":{},"data":[{"name":"Product A","revenue":1500},{"name":"Product B","revenue":2500}]}`
	var buf bytes.Buffer
	err := Format(&buf, []byte(data), "table")
	if err != nil {
		t.Fatalf("Format() error: %v", err)
	}

	output := buf.String()
	// Verify output contains the expected values
	for _, want := range []string{"Product A", "Product B", "1500", "2500"} {
		if !strings.Contains(output, want) {
			t.Errorf("table output missing %q\noutput:\n%s", want, output)
		}
	}
}

func TestFormat_Table_Object(t *testing.T) {
	data := `{"meta":{},"data":{"total_revenue":5000,"total_bookings":42}}`
	var buf bytes.Buffer
	err := Format(&buf, []byte(data), "table")
	if err != nil {
		t.Fatalf("Format() error: %v", err)
	}

	output := buf.String()
	// Object data should render as Field/Value table
	for _, want := range []string{"total_bookings", "42", "total_revenue", "5000"} {
		if !strings.Contains(output, want) {
			t.Errorf("table output missing %q\noutput:\n%s", want, output)
		}
	}
}

func TestFormat_Table_NullData(t *testing.T) {
	data := `{"meta":{},"data":null}`
	var buf bytes.Buffer
	err := Format(&buf, []byte(data), "table")
	if err != nil {
		t.Fatalf("Format() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No data") {
		t.Errorf("expected 'No data' message, got: %s", output)
	}
}

func TestFormat_Table_EmptyArray(t *testing.T) {
	data := `{"meta":{},"data":[]}`
	var buf bytes.Buffer
	err := Format(&buf, []byte(data), "table")
	if err != nil {
		t.Fatalf("Format() error: %v", err)
	}
	// Empty array should not cause error; output may be raw "[]" fallback
}

func TestFormat_Table_NestedObject(t *testing.T) {
	data := `{"meta":{},"data":{"summary":{"count":10,"total":500},"name":"test"}}`
	var buf bytes.Buffer
	err := Format(&buf, []byte(data), "table")
	if err != nil {
		t.Fatalf("Format() error: %v", err)
	}

	output := buf.String()
	for _, want := range []string{"count", "10", "total", "500", "name", "test"} {
		if !strings.Contains(output, want) {
			t.Errorf("table output missing %q\noutput:\n%s", want, output)
		}
	}
}

func TestFormat_Table_ArrayInObject(t *testing.T) {
	data := `{"meta":{},"data":{"items":[{"id":1,"name":"A"},{"id":2,"name":"B"}],"total":2}}`
	var buf bytes.Buffer
	err := Format(&buf, []byte(data), "table")
	if err != nil {
		t.Fatalf("Format() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "2 items") {
		t.Errorf("expected '[2 items]' in output\noutput:\n%s", output)
	}
}

func TestFormat_CSV_Array(t *testing.T) {
	data := `{"data":[{"name":"Product A","revenue":1500},{"name":"Product B","revenue":2500}]}`
	var buf bytes.Buffer
	err := Format(&buf, []byte(data), "csv")
	if err != nil {
		t.Fatalf("Format() error: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 CSV lines (header + 2 rows), got %d:\n%s", len(lines), output)
	}

	// Check header contains field names (sorted)
	header := lines[0]
	if !strings.Contains(header, "name") || !strings.Contains(header, "revenue") {
		t.Errorf("CSV header missing expected fields: %s", header)
	}

	// Check data rows contain values
	if !strings.Contains(output, "Product A") || !strings.Contains(output, "Product B") {
		t.Errorf("CSV missing expected data values\noutput:\n%s", output)
	}
	if !strings.Contains(output, "1500") || !strings.Contains(output, "2500") {
		t.Errorf("CSV missing expected numeric values\noutput:\n%s", output)
	}
}

func TestFormat_CSV_Object(t *testing.T) {
	data := `{"data":{"total_revenue":5000,"total_bookings":42}}`
	var buf bytes.Buffer
	err := Format(&buf, []byte(data), "csv")
	if err != nil {
		t.Fatalf("Format() error: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 CSV lines (header + 1 row), got %d:\n%s", len(lines), output)
	}

	// Header should have field names
	if !strings.Contains(lines[0], "total_bookings") || !strings.Contains(lines[0], "total_revenue") {
		t.Errorf("CSV header missing expected fields: %s", lines[0])
	}
	// Data row should have values
	if !strings.Contains(output, "42") || !strings.Contains(output, "5000") {
		t.Errorf("CSV missing expected values\noutput:\n%s", output)
	}
}

func TestFormat_CSV_NullData(t *testing.T) {
	data := `{"data":null}`
	var buf bytes.Buffer
	err := Format(&buf, []byte(data), "csv")
	if err != nil {
		t.Fatalf("Format() error: %v", err)
	}

	output := buf.String()
	if output != "" {
		t.Errorf("expected empty output for null data, got: %q", output)
	}
}

func TestFormat_UnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	err := Format(&buf, []byte(`{}`), "xml")
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
	if !strings.Contains(err.Error(), "unknown format") {
		t.Errorf("error = %q, want substring 'unknown format'", err.Error())
	}
}

func TestFormat_InvalidJSON(t *testing.T) {
	var buf bytes.Buffer
	err := Format(&buf, []byte(`not json`), "table")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestFormat_Table_InvalidJSON(t *testing.T) {
	var buf bytes.Buffer
	err := Format(&buf, []byte(`{{{`), "table")
	if err == nil {
		t.Fatal("expected error for invalid JSON in table format")
	}
}

func TestFormat_CSV_InvalidJSON(t *testing.T) {
	var buf bytes.Buffer
	err := Format(&buf, []byte(`{{{`), "csv")
	if err == nil {
		t.Fatal("expected error for invalid JSON in csv format")
	}
}

func TestFormat_Table_EmptyDataString(t *testing.T) {
	data := `{"meta":{},"data":""}`
	var buf bytes.Buffer
	// String data won't match array or object, falls through to raw print
	err := Format(&buf, []byte(data), "table")
	if err != nil {
		t.Fatalf("Format() error: %v", err)
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		name string
		val  interface{}
		want string
	}{
		{"nil", nil, ""},
		{"integer float", float64(42), "42"},
		{"decimal float", float64(3.14), "3.14"},
		{"string", "hello", "hello"},
		{"true", true, "true"},
		{"false", false, "false"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatValue(tt.val)
			if got != tt.want {
				t.Errorf("formatValue(%v) = %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}

func TestSortedKeys(t *testing.T) {
	m := map[string]interface{}{
		"charlie": 3,
		"alpha":   1,
		"bravo":   2,
	}

	keys := sortedKeys(m)
	want := []string{"alpha", "bravo", "charlie"}
	if len(keys) != len(want) {
		t.Fatalf("len(sortedKeys) = %d, want %d", len(keys), len(want))
	}
	for i, k := range keys {
		if k != want[i] {
			t.Errorf("keys[%d] = %q, want %q", i, k, want[i])
		}
	}
}

func TestToAnySlice(t *testing.T) {
	input := []string{"a", "b", "c"}
	result := toAnySlice(input)
	if len(result) != 3 {
		t.Fatalf("len = %d, want 3", len(result))
	}
	for i, v := range result {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("result[%d] is not a string", i)
		}
		if s != input[i] {
			t.Errorf("result[%d] = %q, want %q", i, s, input[i])
		}
	}
}
