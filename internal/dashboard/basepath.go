package dashboard

import (
	"errors"
	"regexp"
	"strings"
)

var basePathCharacters = regexp.MustCompile(`^/[A-Za-z0-9/._~-]*$`)

// normalizeBasePath accepts only unescaped absolute paths and adds one trailing slash.
func normalizeBasePath(value string) (string, error) {
	if value == "" || value == "/" {
		return "/", nil
	}
	path := strings.TrimSuffix(value, "/")
	if !basePathCharacters.MatchString(path) {
		return "", errors.New("dashboard base path must be an unescaped absolute path")
	}
	for _, segment := range strings.Split(path[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("dashboard base path must not contain empty or dot segments")
		}
	}
	return path + "/", nil
}
