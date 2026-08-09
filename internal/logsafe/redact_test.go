package logsafe

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	inputs := []string{
		"BDUSS=secret123",
		"STOKEN: abcdef",
		"Authorization: BearerToken",
		"https://pan.baidu.com/s/x?pwd=3mtz&foo=1",
		"Cookie: BDUSS=abc; STOKEN=xyz",
	}
	for _, input := range inputs {
		got := Redact(input)
		for _, secret := range []string{"secret123", "abcdef", "BearerToken", "3mtz", "BDUSS=abc", "STOKEN=xyz"} {
			if strings.Contains(got, secret) {
				t.Fatalf("secret %q remained in %q -> %q", secret, input, got)
			}
		}
	}
}

func TestLoggerRedactsSensitiveAttrsAndMessage(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelDebug)
	logger.Info("request BDUSS=topsecret", "access_token", "token123", "path", "/safe")
	out := buf.String()
	if strings.Contains(out, "topsecret") || strings.Contains(out, "token123") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, "/safe") {
		t.Fatalf("non-secret attr unexpectedly removed: %s", out)
	}
}
