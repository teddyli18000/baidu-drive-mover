package baidu

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

type ShareContext struct {
	BDSToken string
	ShareID  string
	ShareUK  string
	UK       string
}

func extractShareContext(page []byte) (ShareContext, error) {
	needle := []byte(`"loginstate"`)
	index := bytes.Index(page, needle)
	if index < 0 {
		return ShareContext{}, ErrAuthRequired
	}
	opens := openObjectStack(page, index)
	for i := len(opens) - 1; i >= 0; i-- {
		object, ok := balancedObject(page, opens[i])
		if !ok {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(object))
		decoder.UseNumber()
		var root any
		if err := decoder.Decode(&root); err != nil {
			continue
		}
		if _, ok := findValue(root, "loginstate"); !ok {
			continue
		}
		ctx := ShareContext{
			BDSToken: valueString(root, "bdstoken"),
			ShareID:  valueString(root, "shareid"),
			ShareUK:  valueString(root, "share_uk"),
			UK:       valueString(root, "uk"),
		}
		if ctx.BDSToken != "" && ctx.ShareID != "" && ctx.ShareUK != "" {
			return ctx, nil
		}
	}
	return ShareContext{}, fmt.Errorf("share page did not contain usable login/share metadata: %w", ErrAuthRequired)
}

func openObjectStack(data []byte, stop int) []int {
	var stack []int
	var quote byte
	escaped := false
	if stop > len(data) {
		stop = len(data)
	}
	for i := 0; i < stop; i++ {
		b := data[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if b == '\\' {
				escaped = true
				continue
			}
			if b == quote {
				quote = 0
			}
			continue
		}
		switch b {
		case '"', '\'', '`':
			quote = b
		case '{':
			stack = append(stack, i)
		case '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return stack
}

func balancedObject(data []byte, start int) ([]byte, bool) {
	if start < 0 || start >= len(data) || data[start] != '{' {
		return nil, false
	}
	depth := 0
	var quote byte
	escaped := false
	for i := start; i < len(data); i++ {
		b := data[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if b == '\\' {
				escaped = true
				continue
			}
			if b == quote {
				quote = 0
			}
			continue
		}
		switch b {
		case '"':
			quote = b
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return data[start : i+1], true
			}
		}
	}
	return nil, false
}

func findValue(value any, key string) (any, bool) {
	switch current := value.(type) {
	case map[string]any:
		if found, ok := current[key]; ok {
			return found, true
		}
		for _, child := range current {
			if found, ok := findValue(child, key); ok {
				return found, true
			}
		}
	case []any:
		for _, child := range current {
			if found, ok := findValue(child, key); ok {
				return found, true
			}
		}
	}
	return nil, false
}

func valueString(root any, key string) string {
	value, ok := findValue(root, key)
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}
