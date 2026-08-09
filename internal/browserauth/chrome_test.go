package browserauth

import (
	"strings"
	"testing"

	"github.com/chromedp/cdproto/network"
)

func TestSerializeLoginCookiesRequiresBDUSSAndSTOKEN(t *testing.T) {
	cookies := []*network.Cookie{
		{Name: "BDUSS", Value: "fake-bduss", Domain: ".baidu.com"},
		{Name: "STOKEN", Value: "fake-stoken", Domain: ".baidu.com"},
		{Name: "OTHER", Value: "fake-other", Domain: ".baidu.com"},
		{Name: "FOREIGN", Value: "ignore", Domain: ".example.invalid"},
	}
	value, ok := serializeLoginCookies(cookies)
	if !ok {
		t.Fatal("expected complete login cookies")
	}
	if strings.Contains(value, "FOREIGN") || !strings.Contains(value, "BDUSS=fake-bduss") || !strings.Contains(value, "STOKEN=fake-stoken") {
		t.Fatalf("unexpected serialized cookies: %q", value)
	}
	if _, ok := serializeLoginCookies(cookies[:1]); ok {
		t.Fatal("expected incomplete cookie set to be rejected")
	}
}
