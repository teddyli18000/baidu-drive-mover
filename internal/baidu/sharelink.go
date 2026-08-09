package baidu

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var shareFeaturePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type ShareLink struct {
	Feature   string
	ShortURL  string
	Password  string
	StartPath string
}

func ParseShareLink(raw string) (ShareLink, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ShareLink{}, fmt.Errorf("empty Baidu share link")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ShareLink{}, fmt.Errorf("parse Baidu share link: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host != "pan.baidu.com" && host != "yun.baidu.com" {
		return ShareLink{}, fmt.Errorf("unsupported Baidu share host %q", host)
	}

	var feature string
	switch {
	case strings.HasPrefix(u.Path, "/s/"):
		feature = strings.Trim(strings.TrimPrefix(u.Path, "/s/"), "/")
		if strings.Contains(feature, "/") {
			feature = strings.SplitN(feature, "/", 2)[0]
		}
		decoded, decodeErr := url.PathUnescape(feature)
		if decodeErr != nil {
			return ShareLink{}, fmt.Errorf("decode share key: %w", decodeErr)
		}
		feature = decoded
	case path.Clean(u.Path) == "/share/init":
		feature = strings.TrimSpace(u.Query().Get("surl"))
		if feature != "" && !strings.HasPrefix(feature, "1") {
			feature = "1" + feature
		}
	default:
		return ShareLink{}, fmt.Errorf("unsupported Baidu share link path %q", u.Path)
	}
	if feature == "" || !strings.HasPrefix(feature, "1") || !shareFeaturePattern.MatchString(feature) {
		return ShareLink{}, fmt.Errorf("invalid Baidu share key")
	}

	password := ""
	for _, key := range []string{"pwd", "password", "passcode"} {
		if value := strings.TrimSpace(u.Query().Get(key)); value != "" {
			password = value
			break
		}
	}

	startPath := "/"
	fragment := strings.TrimPrefix(u.Fragment, "?")
	if fragment != "" {
		if values, parseErr := url.ParseQuery(fragment); parseErr == nil {
			if value := strings.TrimSpace(values.Get("list/path")); value != "" {
				startPath = normalizeRemotePath(value)
			}
		}
	}

	return ShareLink{
		Feature:   feature,
		ShortURL:  strings.TrimPrefix(feature, "1"),
		Password:  password,
		StartPath: startPath,
	}, nil
}

func (s ShareLink) SanitizedURL() string {
	base := "https://pan.baidu.com/s/" + s.Feature
	if s.StartPath != "" && s.StartPath != "/" {
		base += "#list/path=" + url.QueryEscape(s.StartPath)
	}
	return base
}

func normalizeRemotePath(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	clean := path.Clean(value)
	if clean == "." || clean == "" {
		return "/"
	}
	return clean
}
