package drive

import (
	"fmt"
	"path"
	"strings"

	"github.com/teddyli18000/baidu-drive-mover/internal/drive/rclone"
)

func validateLogicalPath(value string, allowRoot bool) (string, error) {
	if value == "" || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("invalid logical Drive path")
	}
	if !strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("logical Drive path must be rooted: %q", value)
	}
	clean := path.Clean(value)
	if clean != value {
		return "", fmt.Errorf("logical Drive path is not canonical: %q", value)
	}
	if clean == "/" {
		if allowRoot {
			return clean, nil
		}
		return "", fmt.Errorf("logical Drive root is not valid here")
	}
	for _, component := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		if component == "" || component == "." || component == ".." || strings.ContainsRune(component, '\x00') {
			return "", fmt.Errorf("unsafe logical Drive path component in %q", value)
		}
	}
	return clean, nil
}

func remotePath(logical string) (string, error) {
	clean, err := validateLogicalPath(logical, true)
	if err != nil {
		return "", err
	}
	if clean == "/" {
		return rclone.RemoteName + ":", nil
	}
	return rclone.RemoteName + ":" + strings.TrimPrefix(clean, "/"), nil
}
