//go:build unit

package service

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
)

func TestDiagnosticsReportFixtures(t *testing.T) {
	for _, name := range []string{"complete", "partial", "unusable"} {
		b, e := os.ReadFile("testdata/subproxy/" + name + ".json")
		require.NoError(t, e)
		got, e := parseDiagnosticsReport(b)
		require.NoError(t, e, name)
		require.JSONEq(t, string(b), string(got))
	}
}
func TestDiagnosticsReportValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"version", func(m map[string]any) { m["schema_version"] = 2 }},
		{"missing", func(m map[string]any) { delete(m, "completion") }},
		{"missing_count", func(m map[string]any) { delete(m["completion"].(map[string]any), "executed") }},
		{"null_count", func(m map[string]any) { m["completion"].(map[string]any)["executed"] = nil }},
		{"state", func(m map[string]any) { m["connectivity"] = "good" }},
		{"item_state", func(m map[string]any) { m["items"].([]any)[0].(map[string]any)["status"] = "good" }},
		{"items", func(m map[string]any) {
			items := m["items"].([]any)
			for len(items) < 33 {
				items = append(items, items[0])
			}
			m["items"] = items
		}},
		{"evidence", func(m map[string]any) {
			m["items"].([]any)[0].(map[string]any)["evidence"] = map[string]any{"body": strings.Repeat("x", 2049)}
		}},
		{"nested_evidence", func(m map[string]any) {
			m["items"].([]any)[0].(map[string]any)["evidence"] = map[string]any{"body": map[string]any{"secret": "value"}}
		}},
		{"negative", func(m map[string]any) { m["items"].([]any)[0].(map[string]any)["duration_ms"] = -1 }},
		{"timestamp", func(m map[string]any) { m["checked_at"] = "yesterday" }},
		{"target", func(m map[string]any) { delete(m["items"].([]any)[0].(map[string]any), "target") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var obj map[string]any
			require.NoError(t, json.Unmarshal(diagnosticsFixture(t, "valid", "quick"), &obj))
			tc.mutate(obj)
			b, e := json.Marshal(obj)
			require.NoError(t, e)
			_, e = parseDiagnosticsReport(b)
			require.Error(t, e)
		})
	}
	_, e := parseDiagnosticsReport([]byte(strings.Repeat(" ", 256*1024+1)))
	require.Error(t, e)
}
func TestDiagnosticsReportOptionalAdditionsNotForwarded(t *testing.T) {
	var obj map[string]any
	require.NoError(t, json.Unmarshal(diagnosticsFixture(t, "valid", "quick"), &obj))
	obj["future_optional"] = map[string]any{"password": "private"}
	obj["items"].([]any)[0].(map[string]any)["future_item"] = "private"
	b, e := json.Marshal(obj)
	require.NoError(t, e)
	got, e := parseDiagnosticsReport(b)
	require.NoError(t, e)
	require.NotContains(t, string(got), "private")
}
