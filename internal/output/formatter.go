package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/olekukonko/tablewriter"
)

// ValidFormat returns true if format is a supported output format.
func ValidFormat(format string) bool {
	switch format {
	case "json", "table", "csv":
		return true
	default:
		return false
	}
}

// Format writes the API response in the given format to w.
// format must be "json", "table", or "csv".
func Format(w io.Writer, data []byte, format string) error {
	switch format {
	case "json":
		return formatJSON(w, data)
	case "table":
		return formatTable(w, data)
	case "csv":
		return formatCSV(w, data)
	default:
		return fmt.Errorf("unknown format %q (use json, table, or csv)", format)
	}
}

func formatJSON(w io.Writer, data []byte) error {
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	pretty, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(pretty))
	return err
}

func formatTable(w io.Writer, data []byte) error {
	var envelope struct {
		Meta json.RawMessage `json:"meta"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		fmt.Fprintln(w, "No data returned for this period.")
		return nil
	}

	// Try array first
	var arr []map[string]interface{}
	if err := json.Unmarshal(envelope.Data, &arr); err == nil && len(arr) > 0 {
		return renderArrayTable(w, arr)
	}

	// Try object (aggregate)
	var obj map[string]interface{}
	if err := json.Unmarshal(envelope.Data, &obj); err == nil {
		return renderObjectTable(w, obj)
	}

	// Fallback: print raw
	fmt.Fprintln(w, string(envelope.Data))
	return nil
}

func renderArrayTable(w io.Writer, arr []map[string]interface{}) error {
	if len(arr) == 0 {
		fmt.Fprintln(w, "No data returned for this period.")
		return nil
	}

	headers := sortedKeys(arr[0])

	table := tablewriter.NewTable(w)
	table.Header(toAnySlice(headers)...)

	for _, row := range arr {
		vals := make([]any, len(headers))
		for i, h := range headers {
			vals[i] = formatValue(row[h])
		}
		table.Append(vals...)
	}

	return table.Render()
}

func renderObjectTable(w io.Writer, obj map[string]interface{}) error {
	table := tablewriter.NewTable(w)
	table.Header("Field", "Value")

	keys := sortedKeys(obj)
	for _, k := range keys {
		v := obj[k]
		switch val := v.(type) {
		case map[string]interface{}:
			table.Append(k, "")
			subKeys := sortedKeys(val)
			for _, sk := range subKeys {
				table.Append(fmt.Sprintf("  %s", sk), formatValue(val[sk]))
			}
		case []interface{}:
			if len(val) > 0 {
				table.Append(k, fmt.Sprintf("[%d items]", len(val)))
				if first, ok := val[0].(map[string]interface{}); ok {
					subHeaders := sortedKeys(first)
					table.Append("", strings.Join(subHeaders, " | "))
					for _, item := range val {
						if m, ok := item.(map[string]interface{}); ok {
							vals := make([]string, len(subHeaders))
							for i, h := range subHeaders {
								vals[i] = formatValue(m[h])
							}
							table.Append("", strings.Join(vals, " | "))
						}
					}
				}
			} else {
				table.Append(k, "[]")
			}
		default:
			table.Append(k, formatValue(v))
		}
	}

	return table.Render()
}

func formatCSV(w io.Writer, data []byte) error {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}

	cw := csv.NewWriter(w)

	// Try array
	var arr []map[string]interface{}
	if err := json.Unmarshal(envelope.Data, &arr); err == nil && len(arr) > 0 {
		headers := unionKeys(arr)
		if err := cw.Write(headers); err != nil {
			return err
		}
		for _, row := range arr {
			record := make([]string, len(headers))
			for i, h := range headers {
				record[i] = formatValue(row[h])
			}
			if err := cw.Write(record); err != nil {
				return err
			}
		}
		cw.Flush()
		return cw.Error()
	}

	// Try object (aggregate) — single row
	var obj map[string]interface{}
	if err := json.Unmarshal(envelope.Data, &obj); err == nil {
		headers := sortedKeys(obj)
		if err := cw.Write(headers); err != nil {
			return err
		}
		record := make([]string, len(headers))
		for i, h := range headers {
			record[i] = formatValue(obj[h])
		}
		if err := cw.Write(record); err != nil {
			return err
		}
		cw.Flush()
		return cw.Error()
	}

	return nil
}

func formatValue(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%.2f", val)
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(val)
		return string(b)
	}
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// unionKeys returns sorted keys from the union of all maps in the slice.
func unionKeys(arr []map[string]interface{}) []string {
	seen := make(map[string]struct{})
	for _, m := range arr {
		for k := range m {
			seen[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
