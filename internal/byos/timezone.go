package byos

import (
	"errors"
	"strings"
	"time"
	_ "time/tzdata" // The scratch runtime image has no system zoneinfo.
)

func overlayLocation(name string) (*time.Location, error) {
	if name == "" {
		name = "UTC"
	}
	if name == "Local" || strings.HasPrefix(name, "+") || strings.HasPrefix(name, "-") {
		return nil, errors.New("overlay time zone must be an explicit IANA name")
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, errors.New("invalid overlay time zone")
	}
	return location, nil
}
