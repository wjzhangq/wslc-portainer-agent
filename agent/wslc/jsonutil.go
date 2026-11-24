package wslc

import (
	"bytes"
	"encoding/json"
	"strconv"
)

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return 0
		}
		return i
	default:
		return 0
	}
}

func stringList(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if m := asMap(parent[key]); m != nil {
		return m
	}
	m := map[string]any{}
	parent[key] = m
	return m
}

func parseInspectArray(stdout string) ([]map[string]any, error) {
	raw := bytes.TrimSpace([]byte(stdout))
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] == '[' {
		var arr []map[string]any
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, err
		}
		return arr, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return []map[string]any{obj}, nil
}

func objectID(obj map[string]any, fallback string) string {
	if id := asString(obj["Id"]); id != "" {
		return id
	}
	if id := asString(obj["ID"]); id != "" {
		return id
	}
	return fallback
}
