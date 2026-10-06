//go:build windows

package managed

import "errors"

func trusted(string) error { return errors.New("managed systemd deployments require Linux") }
