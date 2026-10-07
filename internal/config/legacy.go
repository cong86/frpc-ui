package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/ini.v1"
)

// Detect by content, including legacy INI stored in a file named frps.toml.
var legacyCommon = regexp.MustCompile(`(?m)^[ \t]*\[common\][ \t]*(?:[;#].*)?\r?$`)

// This is a read-only projection for observation, not a migrated FRP config.
// Match official FRP's INI parser options; never interpolate environment values
// or follow includes. Keep all original fields for a redacted inspection view.
func parseLegacyServer(raw, role string) (*Document, error) {
	if role != "frps" {
		return nil, errors.New("旧 INI 客户端配置暂不支持；请先在隔离环境生成 TOML 迁移预览")
	}
	if strings.Contains(raw, "{{") {
		return nil, errors.New("旧 INI 含环境模板，无法确认实际参数；请手动提供受保护的已展开配置，不修改原服务")
	}
	if strings.Contains(raw, "%(") {
		return nil, errors.New("旧 INI 含字段插值，当前不能确认展开后的参数；请手动提供受保护的已展开配置")
	}
	f, err := ini.LoadSources(ini.LoadOptions{IgnoreInlineComment: true, AllowBooleanKeys: true}, []byte(raw))
	if err != nil {
		return nil, errors.New("旧 INI 语法无效；错误内容已隐藏，请检查原文件")
	}
	common, err := f.GetSection("common")
	if err != nil {
		return nil, errors.New("旧 INI 缺少 [common] 节")
	}
	legacy := map[string]any{}
	for _, section := range f.Sections() {
		fields := map[string]any{}
		for _, key := range section.Keys() {
			fields[key.Name()] = key.Value() // no INI variable interpolation
		}
		legacy[section.Name()] = fields
	}
	values := map[string]any{"legacyINI": legacy}
	set := func(path string, value any) {
		keys := strings.Split(path, ".")
		m := values
		for _, key := range keys[:len(keys)-1] {
			n, ok := m[key].(map[string]any)
			if !ok {
				n = map[string]any{}
				m[key] = n
			}
			m = n
		}
		m[keys[len(keys)-1]] = value
	}
	// Legacy DashboardAddr defaults to all interfaces. Do not invent a
	// loopback endpoint when the original config did not specify one.
	set("webServer.addr", "0.0.0.0")
	for key, path := range map[string]string{
		"bind_addr": "bindAddr", "proxy_bind_addr": "proxyBindAddr",
		"subdomain_host": "subDomainHost", "dashboard_addr": "webServer.addr",
		"dashboard_user": "webServer.user", "dashboard_pwd": "webServer.password",
		"token": "auth.token", "authentication_method": "auth.method",
		"log_file": "log.to", "log_level": "log.level",
	} {
		if common.HasKey(key) {
			set(path, common.Key(key).Value())
		}
	}
	for key, path := range map[string]string{
		"bind_port": "bindPort", "kcp_bind_port": "kcpBindPort", "quic_bind_port": "quicBindPort",
		"vhost_http_port": "vhostHTTPPort", "vhost_https_port": "vhostHTTPSPort",
		"dashboard_port": "webServer.port", "tcpmux_httpconnect_port": "tcpmuxHTTPConnectPort",
		"max_ports_per_client": "maxPortsPerClient", "max_pool_count": "transport.maxPoolCount",
	} {
		if !common.HasKey(key) {
			continue
		}
		n, e := common.Key(key).Int64()
		if e != nil || n < 0 || (strings.HasSuffix(key, "_port") && n > 65535) {
			// key is from the fixed allowlist, never user-provided error text.
			return nil, fmt.Errorf("旧 INI 的 %s 应为有效的非负整数；原值已隐藏", key)
		}
		set(path, n)
	}
	if common.HasKey("dashboard_tls_mode") {
		enabled, e := common.Key("dashboard_tls_mode").Bool()
		if e != nil {
			return nil, errors.New("旧 INI 的 dashboard_tls_mode 应为布尔值；原值已隐藏")
		}
		if enabled {
			set("webServer.tls", map[string]any{"enabled": true})
		}
	}
	for _, key := range []string{"dashboard_tls_cert_file", "dashboard_tls_key_file"} {
		if common.HasKey(key) && common.Key(key).Value() != "" {
			set("webServer.tls", map[string]any{"configured": true})
		}
	}
	if common.HasKey("allow_ports") && common.Key("allow_ports").Value() != "" {
		ranges := []any{}
		for _, part := range strings.Split(common.Key("allow_ports").Value(), ",") {
			ends := strings.Split(strings.TrimSpace(part), "-")
			if len(ends) > 2 {
				return nil, errors.New("旧 INI 的 allow_ports 格式无效；原值已隐藏")
			}
			start, e := strconv.ParseInt(strings.TrimSpace(ends[0]), 10, 64)
			end := start
			if len(ends) == 2 {
				var endErr error
				end, endErr = strconv.ParseInt(strings.TrimSpace(ends[1]), 10, 64)
				if endErr != nil {
					e = endErr
				}
			}
			if e != nil || start < 1 || end > 65535 || start > end {
				return nil, errors.New("旧 INI 的 allow_ports 端口范围无效；原值已隐藏")
			}
			ranges = append(ranges, map[string]any{"start": start, "end": end})
		}
		set("allowPorts", ranges)
	}
	return &Document{Raw: raw, Values: values, Role: role, Format: "ini"}, nil
}
