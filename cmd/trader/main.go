// Package main is the trader entry point.
//
// Phase 0 占位实现:
//   - 验证 .env 加载
//   - 输出运行模式与配置
//   - 启动一个最小 HTTP server (/health)
//
// 真实实现由 Claude Code 按 Phase 顺序补充:
//   Phase 0  → 配置加载 + DB/Redis 连接 + /health 端点
//   Phase 1  → Collectors 启动
//   ...
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "binance-oi-square-trader: skeleton, awaiting Phase 0 implementation")
	fmt.Fprintln(os.Stderr, "Read CLAUDE.md and start with Phase 0.")
	os.Exit(0)
}
