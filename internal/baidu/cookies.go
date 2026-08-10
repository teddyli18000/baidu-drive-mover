package baidu

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"sort"
	"strings"

	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
)

type CookieStore struct {
	Path     string
	Layout   *runtimepath.Layout
	Relative string
}

func NewCookieStore(layout *runtimepath.Layout) CookieStore {
	return CookieStore{Layout: layout, Relative: "auth/baidu.cookies"}
}

func (s CookieStore) Load() (string, error) {
	if s.Layout != nil {
		file, err := s.Layout.OpenTempFile(s.relativePath(), os.O_RDONLY, 0)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("open Baidu cookie store: %w", err)
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 1<<20))
		if err != nil {
			return "", fmt.Errorf("read Baidu cookie store: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	if s.Path == "" {
		return "", errors.New("cookie store path is empty")
	}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read Baidu cookie store: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func (s CookieStore) Save(value string) error {
	if s.Layout != nil {
		file, err := s.Layout.OpenTempFile(s.relativePath(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("open Baidu cookie store: %w", err)
		}
		return writeCookieFile(file, value)
	}
	if s.Path == "" {
		return errors.New("cookie store path is empty")
	}
	file, err := os.OpenFile(s.Path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open Baidu cookie store: %w", err)
	}
	return writeCookieFile(file, value)
}

func (s CookieStore) relativePath() string {
	if strings.TrimSpace(s.Relative) == "" {
		return "auth/baidu.cookies"
	}
	return s.Relative
}

func writeCookieFile(file *os.File, value string) error {
	if _, err := file.WriteString(strings.TrimSpace(value)); err != nil {
		file.Close()
		return fmt.Errorf("write Baidu cookie store: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync Baidu cookie store: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Baidu cookie store: %w", err)
	}
	return nil
}

func newCookieJar(base *url.URL, header string) (*cookiejar.Jar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	cookies := parseCookieHeader(header)
	if isBaiduServiceHost(base.Hostname()) {
		for _, cookie := range cookies {
			cookie.Domain = ".baidu.com"
		}
	}
	if len(cookies) > 0 {
		jar.SetCookies(base, cookies)
	}
	return jar, nil
}

func isBaiduServiceHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "baidu.com" || strings.HasSuffix(host, ".baidu.com")
}

func parseCookieHeader(header string) []*http.Cookie {
	var cookies []*http.Cookie
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t\r\n") {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: name, Value: strings.TrimSpace(value), Path: "/"})
	}
	return cookies
}

func cookieString(cookies []*http.Cookie) string {
	values := make(map[string]string, len(cookies))
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		values[cookie.Name] = cookie.Value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; ")
}
