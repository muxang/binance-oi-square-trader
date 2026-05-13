# Binance USDⓈ-M Futures 官方文档 URL 索引

> **使用方式**:Claude Code 写涉及对应接口的代码前,必须 `web_fetch` 对应 URL 取得最新文档。
> **不要**凭训练数据里的记忆写参数和返回结构。

---

## 顶层入口

| 用途 | URL |
|---|---|
| Derivatives 文档总入口 | https://developers.binance.com/docs/derivatives/Introduction |
| USDⓈ-M Futures General Info | https://developers.binance.com/docs/derivatives/usds-margined-futures/general-info |
| **Change Log(每次开新模块前必读)** | https://developers.binance.com/docs/derivatives/change-log |
| 错误码表 | https://developers.binance.com/docs/derivatives/usds-margined-futures/error-code |
| Common Definitions | https://developers.binance.com/docs/derivatives/usds-margined-futures/common-definition |

---

## Market Data REST API

| Endpoint | URL |
|---|---|
| 索引页 | https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api |
| **Exchange Information** (`GET /fapi/v1/exchangeInfo`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Exchange-Information |
| **Kline / Candlestick** (`GET /fapi/v1/klines`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Kline-Candlestick-Data |
| **24hr Ticker** (`GET /fapi/v1/ticker/24hr`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/24hr-Ticker-Price-Change-Statistics |
| **Symbol Price Ticker V2** (`GET /fapi/v2/ticker/price`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Symbol-Price-Ticker-v2 |
| **Open Interest** (`GET /fapi/v1/openInterest`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Open-Interest |
| **Open Interest Statistics** (`GET /futures/data/openInterestHist`) ⭐ | https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Open-Interest-Statistics |
| Top Trader Long Short Position Ratio | https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Top-Trader-Long-Short-Ratio |
| Top Trader Long Short Account Ratio | https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Top-Long-Short-Account-Ratio |
| Get Funding Rate History | https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Get-Funding-Rate-History |

⭐ = 本项目核心信号源。

---

## Trade REST API

| Endpoint | URL |
|---|---|
| 索引页 | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api |
| **New Order** (`POST /fapi/v1/order`) ⭐ | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api |
| **Cancel Order** (`DELETE /fapi/v1/order`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Cancel-Order |
| Cancel All Open Orders | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Cancel-All-Open-Orders |
| Query Order | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Query-Order |
| Query Current All Open Orders | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Current-All-Open-Orders |
| **Change Margin Type** (`POST /fapi/v1/marginType`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Change-Margin-Type |
| **Change Initial Leverage** (`POST /fapi/v1/leverage`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Change-Initial-Leverage |
| **Position Information V3** (`GET /fapi/v3/positionRisk`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Position-Information-V3 |
| Account Trade List | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Account-Trade-List |
| Test New Order(测试不真下单) | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/New-Order-Test |

### 2025-12-09 后启用的 Algo Service 接口 ⚠️

| Endpoint | URL |
|---|---|
| **New Algo Order** (`POST /fapi/v1/algoOrder`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/New-Algo-Order |
| Cancel Algo Order (`DELETE /fapi/v1/algoOrder`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Cancel-Algo-Order |
| Cancel All Algo Open Orders (`DELETE /fapi/v1/algoOpenOrders`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Cancel-All-Algo-Open-Orders |
| Query Algo Order (`GET /fapi/v1/algoOrder`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Query-Algo-Order |
| Current All Algo Open Orders (`GET /fapi/v1/openAlgoOrders`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api/Current-All-Algo-Open-Orders |

> **2025-12-09 起**,STOP_MARKET / TAKE_PROFIT_MARKET / STOP / TAKE_PROFIT / TRAILING_STOP_MARKET
> 必须通过 Algo Service 接口下单。本项目"灾难止损"依赖此类型,**必须**实现日期感知切换。

#### ⚠️ Algo Service 与 Regular Order 参数命名差异

**2026-05-13 真盘 catch** (commit `765834a`): `/fapi/v1/algoOrder` 和 `/fapi/v1/order`
对**同一概念**用**不同 param 名**。Binance 对未知 param 静默忽略 → 用默认值,trader 代
码以为下成功了实际行为完全不同。已知差异表:

| 概念 | `/fapi/v1/order` (regular) | `/fapi/v1/algoOrder` (Algo Service) |
|---|---|---|
| Trail 激活价 | `activationPrice` (有 `ion`) | `activatePrice` (无 `ion`) ⚠️ |
| Stop / TP 触发价 | `stopPrice` | `triggerPrice` ⚠️ |
| 客户端订单 ID | `newClientOrderId` | `clientAlgoId` ⚠️ |
| 数量精度要求 | LOT_SIZE.stepSize (整数对齐 alts) | 同 — Binance -1111 LOT_SIZE on violation |
| 价格精度要求 | PRICE_FILTER.tickSize | 同 — Binance -1111 PRICE_FILTER on violation |

**Response 字段命名**: Algo Service GET 接口返回的 JSON 字段名 = POST 的 param 名。
所以遇到 response 解析时,struct tag 必须与 POST 用相同的命名。

**红线检查**: 在 `internal/binance/orders.go` 内,任何接近 `/fapi/v1/algoOrder` 调用的
代码出现 `activationPrice` / `stopPrice` / `newClientOrderId` 即为 bug,会导致 Binance
silently 用默认值。grep:

```
grep -nE "algoOrder" internal/binance/orders.go | head
grep -nE "activationPrice|stopPrice" internal/binance/orders.go  # 应只出现在 /fapi/v1/order 周围
```

历史影响范围(v0.2 Round 1 trail 上线 ~ 2026-05-13 20:32 fix):
- 4 笔 mu 真盘 trail S1 全部以 `activatePrice = mark@fill` 落单(默认值),非设计的 entry × Pct
- Round 2.z 阈值提升 (3/15/30/60 → 5/20/35/65 %) 客户端正确但**完全没传到 Binance**

---

## Account REST API

| Endpoint | URL |
|---|---|
| 索引页 | https://developers.binance.com/docs/derivatives/usds-margined-futures/account/rest-api |
| Futures Account Balance V2 | https://developers.binance.com/docs/derivatives/usds-margined-futures/account/rest-api/Futures-Account-Balance-V2 |

---

## User Data Streams (WebSocket)

| 主题 | URL |
|---|---|
| **Connect 总览** | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams |
| Start (`POST /fapi/v1/listenKey`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Start-User-Data-Stream |
| Keepalive (`PUT /fapi/v1/listenKey`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Keepalive-User-Data-Stream |
| Close (`DELETE /fapi/v1/listenKey`) | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Close-User-Data-Stream |
| **Event: ORDER_TRADE_UPDATE** ⭐ | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Event-Order-Update |
| **Event: ACCOUNT_UPDATE** ⭐ | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Event-Balance-and-Position-Update |
| Event: MARGIN_CALL | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Event-Margin-Call |
| Event: TRADE_LITE | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Event-Trade-Lite |
| Event: User Data Stream Expired | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Event-User-Data-Stream-Expired |
| Event: Account Configuration Update | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Event-Account-Configuration-Update-previous-Leverage-Update |
| Event: Conditional Order Trigger Reject | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Event-Conditional-Order-Trigger-Reject |
| **Event: Algo Order Update** (12-09 后启用) | https://developers.binance.com/docs/derivatives/usds-margined-futures/user-data-streams/Event-Algo-Order-Update |

---

## WebSocket Market Streams(可选,本项目暂不用)

| 主题 | URL |
|---|---|
| 索引 | https://developers.binance.com/docs/derivatives/usds-margined-futures/websocket-market-streams |

> 本项目数据采集走 REST 轮询足矣,不引入 market WS,降低复杂度。

---

## 网络环境

```
REST Production:    https://fapi.binance.com
REST Testnet:       https://testnet.binancefuture.com
WS Production:      wss://fstream.binance.com
WS Testnet:         wss://stream.binancefuture.com
```

Testnet 注册:https://testnet.binancefuture.com

> ⚠️ **代理说明**:本项目批量采集币安全部合约时,单 IP 容易触发限流或地区封锁。
> 详见 `ARCHITECTURE.md §9.5 代理`。所有 REST + WS 必须通过 `internal/binance` 的
> ProxyManager,业务代码不直接构造 HTTP client 或 WS Dialer。

> ⚠️ **运行模式**:`testnet` 模式下,**读接口走主网拿真实数据**,写接口走 testnet
> 测试下单。详见 `ARCHITECTURE.md §11.5`。

---

## 关键约束(摘录,仍以 web_fetch 拿到的最新文档为准)

### 速率限制
- IP 维度 `REQUEST_WEIGHT`: 2400 / 1min
- IP 维度 `ORDERS`: 1200 / 1min
- 收到 429 必须立即退避,继续违反升级 418 IP ban
- 详见 General Info 页面最新数据

### Symbol Filters(下单 precision 强制)
来自 `GET /fapi/v1/exchangeInfo` symbols[].filters,本项目通过 `SymbolService`
缓存 1h(`internal/binance/symbol_service.go`)。下单前必须 round:
- `PRICE_FILTER.tickSize`:price / triggerPrice / stopPrice 必须是 tickSize 倍数
  · Algo Service triggerPrice 也要 round(Round 1 实测 -1111)
  · 我方实施:`floor(price / tickSize) × tickSize`
- `LOT_SIZE.stepSize`:quantity 必须是 stepSize 倍数
  · `internal/decision/sizing.go` 在 SizeTrade 里 `stepRoundDown`
- `LOT_SIZE.minQty`:数量下限。不满足 → reject sizing
- `MIN_NOTIONAL.notional`:订单价值下限(BTC 期货 = 5 USDT;每币种不同)
  · Round 2 smoke 实测 LIMIT BUY 0.01 ETH @ price=100 = 1 USDT < 20 USDT min → -4164

### 时间戳
- 毫秒精度
- 默认 `recvWindow=5000ms`,最大 `60000ms`
- 本项目代理路径偶发延迟尖峰 > 5000ms → Round 1 实测 -1021,Round 1 follow-up
  `internal/binance/client.go` 显式设 `recvWindow=60000`(全部 signed 请求)
- 服务器时间偏差 > recvWindow 会被拒,必须做时间同步

### 订单 client id 规则
- `newClientOrderId` 正则:`^[\.A-Z\:/a-z0-9_-]{1,36}$`
- 本项目实际命名(Round 1+ 实施):
  - 入场单:`t{signal_id}_r{retry_count}` (e.g. `t4878_r0`)
    · `retry_count` 由 `trades.retry_count` 列(migration 0003)驱动
    · Round 1 始终 `r0`;Round 2 -4116 处理时 bump 到 `r1+`
  - Algo Service / 部分止盈 / 移动止损等命名将在 Phase 4 Round 5+ 实施时确定
    · (Round 0 设计中的 `boss-*` 命名为 Phase 5 占位,Round 5 真用时复审)

### 特殊错误处理(必读)
- `-1006 / -1007 UNKNOWN/TIMEOUT`:**API 可能已成功**,必须查询订单状态确认
- `-2011 / -2013`:订单不存在,**当成已撤销/已成交**
- `-4046 / -4059`:幂等错误,**当成成功**
- `-2014 / -2015 / -1022`:**致命**,进程级告警 + 暂停所有 API
- `-1021 Timestamp outside recvWindow`:Round 1 实测代理延迟引起。已设
  `recvWindow=60000`;再发生时 `internal/binance/retry.go` 单次重试
- `-1111 Precision is over the maximum`:Round 1 实测 stop_price 未 round 到
  symbol tickSize 引起。Algo Service triggerPrice / 限价单 price 必须
  `floor(p / tickSize) × tickSize`(`SymbolService.TradingFilters.TickSize`)
- `-2019 Margin is insufficient`:不重试,标 trades.status='failed'
- `-4048 Margin type cannot be changed if there exists position`:Round 2
  smoke 实测,持仓存在时 setMarginType 拒绝。当前归类 ActionPermanent
- `-4116 ClientOrderId is duplicated`:**Round 2 实测真错误码**(NOT -2022,
  老 references 中 -2022 是另一场景)。order 仍 NEW/PARTIALLY_FILLED 时重发
  相同 `clientOrderId` 触发;`internal/binance/orders.go` 走
  `GetOrderByClientID` lookup 路径返回已存在的 order(idempotent recovery)
- 详细决策矩阵在写代码前 web_fetch 错误码页面校对一次

---

## 引用样例(代码注释格式)

```go
// 单引用
// ref: references/binance/urls.md §「Open Interest Statistics」
// docs: https://developers.binance.com/docs/derivatives/usds-margined-futures/market-data/rest-api/Open-Interest-Statistics

// 多引用
// refs:
//   - references/binance/urls.md §「New Order」
//     https://developers.binance.com/docs/derivatives/usds-margined-futures/trade/rest-api
//   - references/binance/urls.md §「Error Codes」
//     https://developers.binance.com/docs/derivatives/usds-margined-futures/error-code
```
