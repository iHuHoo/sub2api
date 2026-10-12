package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

var diagnosticsLabel = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
var diagnosticsVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?(?:\+[a-zA-Z0-9.-]+)?$`)
var diagnosticsRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var diagnosticsRuleset = regexp.MustCompile(`^[a-f0-9]{64}$`)
var errDiagnosticsReport = errors.New("invalid diagnostics report")

// Only known contract fields are forwarded. Optional additions in schema v1 are ignored.
func diagnosticsObject(raw json.RawMessage, required, optional string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, errDiagnosticsReport
	}
	clean := make(map[string]json.RawMessage)
	for _, key := range strings.Fields(required) {
		value, ok := obj[key]
		if !ok || (bytes.Equal(bytes.TrimSpace(value), []byte("null")) && key != "exit") {
			return nil, errDiagnosticsReport
		}
		clean[key] = value
	}
	for _, key := range strings.Fields(optional) {
		if v, ok := obj[key]; ok {
			if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return nil, errDiagnosticsReport
			}
			clean[key] = v
		}
	}
	return clean, nil
}
func diagnosticsString(obj map[string]json.RawMessage, key string, max int, pattern *regexp.Regexp, values ...string) error {
	raw, ok := obj[key]
	if !ok {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || len(s) > max {
		return errDiagnosticsReport
	}
	if pattern != nil && !pattern.MatchString(s) {
		return errDiagnosticsReport
	}
	if len(values) > 0 {
		for _, v := range values {
			if s == v {
				return nil
			}
		}
		return errDiagnosticsReport
	}
	return nil
}
func diagnosticsNumber(obj map[string]json.RawMessage, key string, max float64, integer bool) error {
	raw, ok := obj[key]
	if !ok {
		return nil
	}
	var n float64
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &n) != nil || n < 0 || n > max || (integer && math.Trunc(n) != n) {
		return errDiagnosticsReport
	}
	return nil
}
func diagnosticsTime(obj map[string]json.RawMessage, key string) error {
	var s string
	if json.Unmarshal(obj[key], &s) != nil {
		return errDiagnosticsReport
	}
	if _, e := time.Parse(time.RFC3339Nano, s); e != nil {
		return errDiagnosticsReport
	}
	return nil
}
func diagnosticsStatus(obj map[string]json.RawMessage) error {
	if e := diagnosticsString(obj, "status", 16, nil, "pass", "warn", "fail", "challenge", "unknown", "skipped"); e != nil {
		return e
	}
	return diagnosticsString(obj, "reason_code", 64, diagnosticsLabel)
}
func diagnosticsExit(raw json.RawMessage) (json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return raw, nil
	}
	obj, e := diagnosticsObject(raw, "ip source observed_at", "country_code country region city asn organization")
	if e != nil {
		return nil, e
	}
	for key := range obj {
		if diagnosticsString(obj, key, 2048, nil) != nil {
			return nil, errDiagnosticsReport
		}
	}
	if diagnosticsTime(obj, "observed_at") != nil {
		return nil, errDiagnosticsReport
	}
	return json.Marshal(obj)
}
func diagnosticsSamples(raw json.RawMessage) (json.RawMessage, error) {
	var samples []json.RawMessage
	if json.Unmarshal(raw, &samples) != nil || samples == nil || len(samples) > 32 {
		return nil, errDiagnosticsReport
	}
	for i, v := range samples {
		obj, e := diagnosticsObject(v, "status reason_code", "ip source latency_ms")
		if e != nil {
			return nil, e
		}
		if diagnosticsStatus(obj) != nil || diagnosticsString(obj, "ip", 2048, nil) != nil || diagnosticsString(obj, "source", 2048, nil) != nil || diagnosticsNumber(obj, "latency_ms", math.MaxInt64, true) != nil {
			return nil, errDiagnosticsReport
		}
		samples[i], _ = json.Marshal(obj)
	}
	return json.Marshal(samples)
}
func diagnosticsStability(raw json.RawMessage) (json.RawMessage, error) {
	obj, e := diagnosticsObject(raw, "status reason_code attempted succeeded distinct_ips samples", "latency_min_ms latency_median_ms latency_max_ms")
	if e != nil {
		return nil, e
	}
	if diagnosticsStatus(obj) != nil {
		return nil, errDiagnosticsReport
	}
	for _, k := range []string{"attempted", "succeeded", "distinct_ips", "latency_min_ms", "latency_max_ms"} {
		if diagnosticsNumber(obj, k, math.MaxInt64, true) != nil {
			return nil, errDiagnosticsReport
		}
	}
	if diagnosticsNumber(obj, "latency_median_ms", math.MaxFloat64, false) != nil {
		return nil, errDiagnosticsReport
	}
	obj["samples"], e = diagnosticsSamples(obj["samples"])
	if e != nil {
		return nil, e
	}
	return json.Marshal(obj)
}
func diagnosticsItem(raw json.RawMessage) (json.RawMessage, error) {
	obj, e := diagnosticsObject(raw, "id plugin_id plugin_version rule_id rule_version category target status reason_code message started_at", "duration_ms http_status cf_ray evidence exit samples")
	if e != nil {
		return nil, e
	}
	for _, k := range []string{"id", "plugin_id", "rule_id", "category"} {
		if diagnosticsString(obj, k, 64, diagnosticsLabel) != nil {
			return nil, errDiagnosticsReport
		}
	}
	for _, k := range []string{"plugin_version", "rule_version"} {
		if diagnosticsString(obj, k, 128, diagnosticsVersion) != nil {
			return nil, errDiagnosticsReport
		}
	}
	for _, k := range []string{"target", "message"} {
		if diagnosticsString(obj, k, 2048, nil) != nil {
			return nil, errDiagnosticsReport
		}
	}
	if diagnosticsStatus(obj) != nil || diagnosticsTime(obj, "started_at") != nil || diagnosticsString(obj, "cf_ray", 64, nil) != nil || diagnosticsNumber(obj, "duration_ms", math.MaxInt64, true) != nil || diagnosticsNumber(obj, "http_status", 599, true) != nil {
		return nil, errDiagnosticsReport
	}
	if v, ok := obj["http_status"]; ok {
		var n int
		_ = json.Unmarshal(v, &n)
		if n < 100 {
			return nil, errDiagnosticsReport
		}
	}
	if v, ok := obj["evidence"]; ok {
		var evidence map[string]any
		if json.Unmarshal(v, &evidence) != nil || evidence == nil || len(evidence) > 32 || len(v) > 16*1024 {
			return nil, errDiagnosticsReport
		}
		for k, value := range evidence {
			if !diagnosticsLabel.MatchString(k) {
				return nil, errDiagnosticsReport
			}
			switch x := value.(type) {
			case string:
				if len(x) > 2048 {
					return nil, errDiagnosticsReport
				}
			case bool, float64:
			default:
				return nil, errDiagnosticsReport
			}
		}
	}
	if v, ok := obj["exit"]; ok {
		obj["exit"], e = diagnosticsExit(v)
		if e != nil {
			return nil, e
		}
	}
	if v, ok := obj["samples"]; ok {
		obj["samples"], e = diagnosticsSamples(v)
		if e != nil {
			return nil, e
		}
	}
	return json.Marshal(obj)
}
func parseDiagnosticsReport(raw []byte) (json.RawMessage, error) {
	if len(raw) > 256*1024 {
		return nil, errDiagnosticsReport
	}
	obj, e := diagnosticsObject(raw, "schema_version ruleset_version request_id checked_at mode execution connectivity exit stability items summary completion", "")
	if e != nil {
		return nil, e
	}
	var version int
	if json.Unmarshal(obj["schema_version"], &version) != nil || version != 1 {
		return nil, errDiagnosticsReport
	}
	for _, check := range []error{
		diagnosticsString(obj, "ruleset_version", 64, diagnosticsRuleset),
		diagnosticsString(obj, "request_id", 64, diagnosticsRequestID),
		diagnosticsTime(obj, "checked_at"),
		diagnosticsString(obj, "mode", 6, nil, "quick", "sample"),
		diagnosticsString(obj, "execution", 9, nil, "completed", "partial"),
		diagnosticsString(obj, "connectivity", 9, nil, "unusable", "degraded", "reachable", "unknown"),
		diagnosticsString(obj, "summary", 2048, nil),
	} {
		if check != nil {
			return nil, check
		}
	}
	obj["exit"], e = diagnosticsExit(obj["exit"])
	if e != nil {
		return nil, e
	}
	obj["stability"], e = diagnosticsStability(obj["stability"])
	if e != nil {
		return nil, e
	}
	var items []json.RawMessage
	if json.Unmarshal(obj["items"], &items) != nil || items == nil || len(items) > 32 {
		return nil, errDiagnosticsReport
	}
	for i, v := range items {
		items[i], e = diagnosticsItem(v)
		if e != nil {
			return nil, fmt.Errorf("item: %w", e)
		}
	}
	obj["items"], _ = json.Marshal(items)
	completion, e := diagnosticsObject(obj["completion"], "planned executed conclusive unknown skipped", "")
	if e != nil {
		return nil, e
	}
	for k := range completion {
		if diagnosticsNumber(completion, k, 32, true) != nil {
			return nil, errDiagnosticsReport
		}
	}
	obj["completion"], _ = json.Marshal(completion)
	return json.Marshal(obj)
}
