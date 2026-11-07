package main

// @title Grok2API
// @version 1.0
// @description Grok Build 与 Grok Web 多账号 API 网关。
// @BasePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description 使用 "Bearer g2a_xxx_xxx"。

import (
	"fmt"
	"os"
	// 内嵌 IANA 时区库，非 Docker 或 Windows 主机缺少 tzdata 时仪表盘时区参数仍可解析。
	_ "time/tzdata"

	"github.com/chenyme/grok2api/backend/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
