# binance-oi-square-trader

币安 USDⓈ-M 永续合约自动化交易系统。

> 🚨 **生产级实盘项目,1000 USDT 真实资金。代码缺陷 = 经济损失。**

## 文档导航

| 文件 | 用途 |
|---|---|
| [`SPEC.md`](./SPEC.md) | 业务需求规格(做什么) |
| [`ARCHITECTURE.md`](./ARCHITECTURE.md) | 技术架构(怎么做) |
| [`CLAUDE.md`](./CLAUDE.md) | Claude Code 工作守则 |
| [`references/`](./references/) | 外部信息源索引(API、参考代码) |
| `RUNBOOK.md` | 运维手册(Phase 6 后补) |

## 给 Claude Code 的开场白(复制粘贴用)

> 我要开发一个币安自动化交易系统。仓库根目录的 `SPEC.md` `ARCHITECTURE.md` `CLAUDE.md` 是必读三件套,`references/` 是外部信息源唯一索引。
>
> 现在从 **Phase 0** 开始。请先 view 这四份文档,然后:
> 1. 确认你理解 CLAUDE.md 的所有约束(列 5 条最重要的)
> 2. 列出 Phase 0 的工作清单
> 3. 等我确认后再动手
>
> 注意:
> - 写代码前 web_fetch references 索引中对应的官方文档
> - 遇到不确定的地方停下问我
> - 单次响应不超过 3 个文件 / 200 行

## 快速开始(Phase 0 完成后)

### 本地开发(Windows + Docker Desktop)

```bash
# 安装依赖
make bootstrap

# 启动本地基础设施(PG / Redis / Prometheus / Grafana / Loki)
make docker-up

# 运行 DB 迁移
make migrate

# 启动应用(默认 testnet 模式)
make dev
```

### VPS 一键部署(Ubuntu 22.04+)

```bash
# 第一次部署:
git clone <repo> && cd binance-oi-square-trader
cp .env.example .env
nano .env   # 填入 BINANCE_API_KEY / TG_BOT_TOKEN / 代理 等
sudo bash scripts/bootstrap.sh   # 装 docker + 起所有服务 + 跑迁移

# 后续更新:
git pull
bash scripts/deploy.sh

# 健康检查:
bash scripts/healthcheck.sh
```

部署细节见 [`scripts/README.md`](./scripts/README.md)。

---

访问:
- Trader API: http://localhost:8080/health
- Dashboard:  http://localhost:3000
- Grafana:    http://localhost:3001 (admin/admin)
- Prometheus: http://localhost:9090

## 运行模式(`TRADER_MODE`)

| 值 | 含义 | 何时用 |
|---|---|---|
| `testnet` (默认) | 读接口走主网拿真实数据,写接口走 testnet 测试下单 | 开发、Phase 1-5 |
| `mainnet` | 主网真实下单 | Phase 6+ |

切换到 `mainnet` 必须同时设置 `TRADER_MAINNET_CONFIRM=I_UNDERSTAND`,否则进程立即退出。

## 实盘部署 — 代理池强制要求

Phase 1 collector 单 IP 5 分钟总请求量(全采集 ~529 USDⓈ-M perp):

| Collector | 频率 | 单 IP 5min 请求量 |
|---|---|---|
| T1 OI history (`openInterestHist`) | 5min | ~529(占 1000 req/5min/IP 限的 53%) |
| T7 K线 + ATR/EMA (`klines`) | 5min | ~529(weight=1,占 12000 weight/5min 的 4.4%) |
| T6 BTC regime | 1min | ~5 |
| **合计** | | **~1063 req/5min/IP** |

实盘部署**强制要求** `BINANCE_PROXY_MODE=pool` 且代理池 ≥ 2 个代理:

```env
BINANCE_PROXY_MODE=pool
BINANCE_PROXY_POOL_URLS=http://proxy1.example.com:8080,http://proxy2.example.com:8080
BINANCE_PROXY_POOL_STRATEGY=round_robin
```

开发/测试环境可用 `BINANCE_PROXY_MODE=none` 直连,但需要降低采集范围或频率(如 T1 改 `*/10 * * * *`)避免撞 IP 限流。

## 项目结构

详见 [`ARCHITECTURE.md`](./ARCHITECTURE.md#6-目录结构)。

## 开发规范

详见 [`CLAUDE.md`](./CLAUDE.md)。

## License

私有项目。
