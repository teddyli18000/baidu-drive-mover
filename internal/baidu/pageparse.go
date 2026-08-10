package baidu

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type ShareContext struct {
	BDSToken string
	ShareID  string
	ShareUK  string
	UK       string
}

const (
	maxEmbeddedShareContextDepth = 3
	maxShareContextScanBytes     = 48 << 20
	maxShareContextObjectStarts  = 32
	maxShareContextObjectBytes   = 48 << 20
	maxShareContextObjects       = 64
)

func extractShareContext(page []byte) (ShareContext, error) {
	state := shareContextScanState{
		remainingBytes:       maxShareContextScanBytes,
		remainingObjectBytes: maxShareContextObjectBytes,
		remainingObjects:     maxShareContextObjects,
		seen:                 make(map[[sha256.Size]byte]struct{}),
	}
	ctx, markerFound, ok := extractShareContextAtDepth(page, 0, &state)
	if ok {
		return ctx, nil
	}
	if !markerFound {
		return ShareContext{}, ErrAuthRequired
	}
	return ShareContext{}, fmt.Errorf("share page did not contain usable login/share metadata: %w", ErrAuthRequired)
}

type shareContextScanState struct {
	remainingBytes       int
	remainingObjectBytes int
	remainingObjects     int
	seen                 map[[sha256.Size]byte]struct{}
}

func extractShareContextAtDepth(data []byte, depth int, state *shareContextScanState) (ShareContext, bool, bool) {
	if depth > maxEmbeddedShareContextDepth || state == nil || len(data) == 0 || len(data) > state.remainingBytes {
		return ShareContext{}, false, false
	}
	digest := sha256.Sum256(data)
	if _, exists := state.seen[digest]; exists {
		return ShareContext{}, false, false
	}
	state.seen[digest] = struct{}{}
	state.remainingBytes -= len(data)

	needle := []byte("loginstate")
	markerFound := false
	for searchFrom := 0; searchFrom < len(data); {
		relative := bytes.Index(data[searchFrom:], needle)
		if relative < 0 {
			break
		}
		markerFound = true
		index := searchFrom + relative
		opens := openObjectStack(data, index)
		if len(opens) > maxShareContextObjectStarts {
			opens = opens[len(opens)-maxShareContextObjectStarts:]
		}
		for i := len(opens) - 1; i >= 0; i-- {
			if state.remainingObjects <= 0 || state.remainingObjectBytes <= 0 {
				return ShareContext{}, markerFound, false
			}
			object, balanced := balancedObject(data, opens[i], state.remainingObjectBytes)
			if !balanced {
				continue
			}
			state.remainingObjects--
			state.remainingObjectBytes -= len(object)
			decoder := json.NewDecoder(bytes.NewReader(object))
			decoder.UseNumber()
			var root any
			if err := decoder.Decode(&root); err != nil {
				continue
			}
			if ctx, ok := shareContextFromRoot(root); ok {
				return ctx, true, true
			}
			if depth < maxEmbeddedShareContextDepth {
				for _, embedded := range embeddedShareContextCandidates(root) {
					ctx, nestedMarker, ok := extractShareContextAtDepth([]byte(embedded), depth+1, state)
					markerFound = markerFound || nestedMarker
					if ok {
						return ctx, true, true
					}
				}
			}
		}
		searchFrom = index + len(needle)
	}
	return ShareContext{}, markerFound, false
}

func shareContextFromRoot(root any) (ShareContext, bool) {
	if _, ok := findValue(root, "loginstate"); !ok {
		return ShareContext{}, false
	}
	ctx := ShareContext{
		BDSToken: valueString(root, "bdstoken"),
		ShareID:  valueString(root, "shareid"),
		ShareUK:  valueString(root, "share_uk"),
		UK:       valueString(root, "uk"),
	}
	return ctx, ctx.BDSToken != "" && ctx.ShareID != "" && ctx.ShareUK != ""
}

func embeddedShareContextCandidates(value any) []string {
	var candidates []string
	collectEmbeddedShareContextCandidates(value, &candidates)
	return candidates
}

func collectEmbeddedShareContextCandidates(value any, candidates *[]string) {
	switch current := value.(type) {
	case map[string]any:
		for _, child := range current {
			collectEmbeddedShareContextCandidates(child, candidates)
		}
	case []any:
		for _, child := range current {
			collectEmbeddedShareContextCandidates(child, candidates)
		}
	case string:
		if strings.Contains(current, `"loginstate"`) &&
			strings.Contains(current, `"bdstoken"`) &&
			strings.Contains(current, `"shareid"`) &&
			strings.Contains(current, `"share_uk"`) {
			*candidates = append(*candidates, current)
		}
	}
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

func balancedObject(data []byte, start, maxBytes int) ([]byte, bool) {
	if start < 0 || start >= len(data) || data[start] != '{' {
		return nil, false
	}
	if maxBytes <= 0 {
		return nil, false
	}
	depth := 0
	var quote byte
	escaped := false
	for i := start; i < len(data); i++ {
		if i-start >= maxBytes {
			return nil, false
		}
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
