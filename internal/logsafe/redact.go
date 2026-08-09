package logsafe

import (
	"io"
	"log/slog"
	"regexp"
	"strings"
)

const redacted = "[REDACTED]"

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(BDUSS|STOKEN|SBOXTKN|BAIDUID|Authorization|refresh_token|access_token)(\s*[:=]\s*)([^\s;&]+)`),
	regexp.MustCompile(`(?i)([?&](?:pwd|password|passcode)=)([^&#\s]+)`),
	regexp.MustCompile(`(?i)(Cookie\s*:\s*)([^\r\n]+)`),
	regexp.MustCompile(`(?i)(Set-Cookie\s*:\s*)([^\r\n]+)`),
}

var sensitiveKeys = map[string]struct{}{
	"authorization":   {},
	"cookie":          {},
	"set-cookie":      {},
	"bduss":           {},
	"stoken":          {},
	"sboxtkn":         {},
	"access_token":    {},
	"refresh_token":   {},
	"extraction_code": {},
	"password":        {},
}

// Redact removes common credential material from text before it reaches logs.
func Redact(s string) string {
	out := s
	for _, re := range patterns {
		out = re.ReplaceAllStringFunc(out, func(match string) string {
			loc := re.FindStringSubmatchIndex(match)
			if len(loc) < 6 {
				return redacted
			}
			if len(loc) >= 8 && loc[2] >= 0 && loc[5] >= 0 {
				prefixEnd := loc[5]
				if loc[4] >= 0 && loc[5] >= 0 {
					prefixEnd = loc[5]
				}
				return match[:prefixEnd] + redacted
			}
			return redacted
		})
	}
	return out
}

// NewLogger creates a slog logger whose attrs and rendered output are redacted.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(&redactingWriter{dst: w}, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if _, ok := sensitiveKeys[strings.ToLower(attr.Key)]; ok {
				return slog.String(attr.Key, redacted)
			}
			if attr.Value.Kind() == slog.KindString {
				return slog.String(attr.Key, Redact(attr.Value.String()))
			}
			return attr
		},
	})
	return slog.New(handler)
}

type redactingWriter struct {
	dst io.Writer
}

func (w *redactingWriter) Write(p []byte) (int, error) {
	clean := []byte(Redact(string(p)))
	_, err := w.dst.Write(clean)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
