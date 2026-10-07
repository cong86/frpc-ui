// Package cli localizes standard flag diagnostics without changing command arguments.
package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

var flagText = strings.NewReplacer(
	"flag provided but not defined: ", "未知参数：",
	"flag needs an argument: ", "参数缺少值：",
	"invalid value ", "参数值无效：",
	"for flag ", "对应参数：",
	"parse error", "解析失败",
	"invalid syntax", "格式无效",
	"value out of range", "数值超出范围",
	"(default ", "(默认值 ",
	" string\n", " 文本\n",
	" int\n", " 整数\n",
	" duration\n", " 时长\n",
)

func Text(message string) string { return flagText.Replace(message) }

type writer struct{ target io.Writer }

func (w writer) Write(p []byte) (int, error) {
	if _, e := io.WriteString(w.target, Text(string(p))); e != nil {
		return 0, e
	}
	return len(p), nil
}

func Configure(fs *flag.FlagSet) {
	fs.SetOutput(writer{fs.Output()})
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "用法：frp-console %s [参数]\n", fs.Name())
		fs.PrintDefaults()
	}
}
