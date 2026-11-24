package wslc

import (
	"encoding/json"
	"strings"
)

func parseFilters(raw string) map[string][]string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	var arrays map[string][]string
	if json.Unmarshal([]byte(raw), &arrays) == nil {
		return arrays
	}
	var sets map[string]map[string]bool
	if json.Unmarshal([]byte(raw), &sets) != nil {
		return nil
	}
	out := make(map[string][]string, len(sets))
	for key, vals := range sets {
		for val, on := range vals {
			if on {
				out[key] = append(out[key], val)
			}
		}
	}
	return out
}

func filterContainers(list []any, raw string) []any {
	filters := parseFilters(raw)
	if len(filters) == 0 || len(list) == 0 {
		return list
	}
	out := make([]any, 0, len(list))
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok || !containerMatches(obj, filters) {
			continue
		}
		out = append(out, obj)
	}
	return out
}

func filterImages(list []map[string]any, raw string) []map[string]any {
	filters := parseFilters(raw)
	if len(filters) == 0 || len(list) == 0 {
		return list
	}
	out := make([]map[string]any, 0, len(list))
	for _, obj := range list {
		if imageMatches(obj, filters) {
			out = append(out, obj)
		}
	}
	return out
}

func containerMatches(obj map[string]any, filters map[string][]string) bool {
	for key, values := range filters {
		if len(values) == 0 {
			continue
		}
		switch key {
		case "status":
			if !matchAny(values, func(value string) bool {
				return strings.EqualFold(value, asString(obj["State"]))
			}) {
				return false
			}
		case "id":
			if !matchAny(values, func(value string) bool {
				id := asString(obj["Id"])
				return value != "" && strings.HasPrefix(id, value)
			}) {
				return false
			}
		case "name":
			if !matchAny(values, func(value string) bool {
				return nameMatches(nameList(obj["Names"]), value)
			}) {
				return false
			}
		case "label":
			if !matchAny(values, func(value string) bool {
				return labelMatches(asMap(obj["Labels"]), value)
			}) {
				return false
			}
		}
	}
	return true
}

func imageMatches(obj map[string]any, filters map[string][]string) bool {
	tags := stringList(obj["RepoTags"])
	for key, values := range filters {
		if len(values) == 0 {
			continue
		}
		switch key {
		case "reference":
			if !matchAny(values, func(value string) bool {
				return referenceMatches(tags, value)
			}) {
				return false
			}
		case "dangling":
			dangling := len(tags) == 0
			if !matchAny(values, func(value string) bool {
				return wantDangling(value) == dangling
			}) {
				return false
			}
		}
	}
	return true
}

func matchAny(values []string, pred func(string) bool) bool {
	for _, value := range values {
		if pred(value) {
			return true
		}
	}
	return false
}

func nameList(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		return stringList(t)
	default:
		return nil
	}
}

func nameMatches(names []string, value string) bool {
	want := strings.TrimPrefix(strings.TrimSpace(value), "/")
	if want == "" {
		return false
	}
	for _, name := range names {
		got := strings.TrimPrefix(name, "/")
		if got == want || strings.HasPrefix(got, want) {
			return true
		}
	}
	return false
}

func labelMatches(labels map[string]any, value string) bool {
	if labels == nil {
		return false
	}
	key, val, hasVal := strings.Cut(value, "=")
	if !hasVal {
		_, ok := labels[key]
		return ok
	}
	return asString(labels[key]) == val
}

func referenceMatches(tags []string, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, tag := range tags {
		if tag == value || strings.HasPrefix(tag, value) {
			return true
		}
	}
	return false
}

func wantDangling(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
