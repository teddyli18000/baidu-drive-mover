package baidu

import "testing"

func TestParseShareLink(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		feature   string
		password  string
		startPath string
	}{
		{
			name:      "standard with password and subpath",
			raw:       "https://pan.baidu.com/s/1Synthetic-Key_9?pwd=a1b2#list/path=%2Fdocs%2Fml&parentPath=%2Fignored",
			feature:   "1Synthetic-Key_9",
			password:  "a1b2",
			startPath: "/docs/ml",
		},
		{
			name:      "scheme omitted",
			raw:       "pan.baidu.com/s/1AnotherSynthetic",
			feature:   "1AnotherSynthetic",
			startPath: "/",
		},
		{
			name:      "share init",
			raw:       "https://pan.baidu.com/share/init?surl=SyntheticInit",
			feature:   "1SyntheticInit",
			startPath: "/",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseShareLink(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got.Feature != tc.feature || got.Password != tc.password || got.StartPath != tc.startPath {
				t.Fatalf("unexpected result: %+v", got)
			}
		})
	}
}

func TestParseShareLinkRejectsUntrustedHost(t *testing.T) {
	if _, err := ParseShareLink("https://example.invalid/s/1Synthetic"); err == nil {
		t.Fatal("expected unsupported host to be rejected")
	}
}

func TestSanitizedURLDoesNotContainPassword(t *testing.T) {
	link, err := ParseShareLink("https://pan.baidu.com/s/1Synthetic?pwd=secret#list/path=%2Ffolder")
	if err != nil {
		t.Fatal(err)
	}
	got := link.SanitizedURL()
	if got == "" || got == link.Password || contains(got, "secret") {
		t.Fatalf("password leaked in sanitized URL: %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
