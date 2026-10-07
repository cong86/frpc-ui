//go:build windows

package managed

import "errors"

func trusted(string) error { return errors.New("受管理的 systemd 部署需要 Linux") }
func TrustedDirectory(string) error {
	return errors.New("受保护的采集元数据需要 Linux")
}
