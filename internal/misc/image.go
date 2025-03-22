package misc

import (
	"fmt"
	"strings"
)

func DecodeImageUrl(image string) (repo string, tag string, err error) {
	if strings.HasPrefix(image, "oci://") {
		image = image[6:]
	}
	a := strings.Split(image, ":")
	if len(a) == 3 {
		// It is host:port/path:version
		return fmt.Sprintf("%s:%s", a[0], a[1]), a[2], nil
	} else if len(a) == 2 {
		// It may be:
		// host/path version (no port)
		// host port/path (no version)
		if strings.Contains(a[0], "/") {
			// it's host/path:version (no port)
			return a[0], a[1], nil
		} else if strings.Contains(a[1], "/") {
			// it's host:port/path (no version)
			return fmt.Sprintf("%s:%s", a[0], a[1]), "latest", nil
		} else {
			return "", "", fmt.Errorf("invalid image name: %s", image)
		}
	} else if len(a) == 1 {
		// it is  host/path  (no port, no version)
		return a[0], "latest", nil
	} else {
		return "", "", fmt.Errorf("invalid image name: %s", image)
	}
}
