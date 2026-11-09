package server

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"example.invalid/hearthlog/internal/model"
)

// attrNode is one typed attribute row in the detail panel.
type attrNode struct {
	Depth       int
	Key         string
	Type        string
	Display     string
	Path        string
	Op          string
	PType       string
	PValue      string
	Filterable  bool
	Unsupported bool
	FilterID    string
}

func walkAttrs(raw []byte) []attrNode {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return []attrNode{{Key: "attributes", Type: "unsupported", Unsupported: true, Display: "Attributes unavailable"}}
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return []attrNode{{Key: "attributes", Type: "unsupported", Unsupported: true, Display: "Attributes unavailable"}}
	}
	var out []attrNode
	walkObject(obj, nil, 0, &out)
	n := 0
	for i := range out {
		if out[i].Filterable {
			n++
			if n == 1 {
				out[i].FilterID = "btn-filter"
			} else {
				out[i].FilterID = "btn-filter-" + itoaAttr(n)
			}
		}
	}
	return out
}

func walkObject(obj map[string]any, path []string, depth int, out *[]attrNode) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		walkValue(key, obj[key], path, depth, out)
	}
}

func walkValue(key string, v any, path []string, depth int, out *[]attrNode) {
	next := append(append([]string{}, path...), key)
	node := attrNode{Depth: depth, Key: key, Path: strings.Join(next, ".")}
	if _, err := model.JSONPath(next); err == nil {
		node.Filterable = true
	}
	switch val := v.(type) {
	case map[string]any:
		node.Type = "object"
		node.Filterable = false
		*out = append(*out, node)
		if len(next) < 8 {
			walkObject(val, next, depth+1, out)
		}
	case []any:
		node.Type = "array"
		node.Filterable = false
		node.Unsupported = true
		node.Display = mustJSON(val)
		*out = append(*out, node)
	case nil:
		node.Type = "null"
		node.Display = "null"
		node.Op = "eq"
		node.PType = "null"
		if !node.Filterable {
			node.Unsupported = true
		}
		*out = append(*out, node)
	case bool:
		node.Type = "boolean"
		if val {
			node.Display = "true"
			node.PValue = "true"
		} else {
			node.Display = "false"
			node.PValue = "false"
		}
		node.Op = "eq"
		node.PType = "boolean"
		if !node.Filterable {
			node.Unsupported = true
		}
		*out = append(*out, node)
	case json.Number:
		text := val.String()
		node.Display = text
		node.PValue = text
		node.Op = "eq"
		if _, err := val.Int64(); err == nil && !strings.ContainsAny(text, ".eE") {
			node.Type = "integer"
			node.PType = "integer"
		} else {
			node.Type = "real"
			node.PType = "real"
		}
		if !node.Filterable {
			node.Unsupported = true
		}
		*out = append(*out, node)
	case string:
		node.Type = "string"
		node.Display = val
		node.PValue = val
		node.Op = "eq"
		node.PType = "string"
		if !node.Filterable {
			node.Unsupported = true
		}
		*out = append(*out, node)
	default:
		node.Type = "unsupported"
		node.Filterable = false
		node.Unsupported = true
		node.Display = mustJSON(val)
		*out = append(*out, node)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func itoaAttr(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
