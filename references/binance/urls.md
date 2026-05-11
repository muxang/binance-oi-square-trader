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

### 时间戳
- 毫秒精度
- 默认 `recvWindow=5000ms`
- 服务器时间偏差 > recvWindow 会被拒,必须做时间同步

### 订单 client id 规则
- `newClientOrderId` 正则:`^[\.A-Z\:/a-z0-9_-]{1,36}$`
- 本项目命名约定(强制):
  - 入场单:`boss-entry-<trade_id>`
  - 灾难止损:`boss-disaster-<trade_id>`
  - 部分止盈:`boss-tp1-<trade_id>` / `boss-tp2-<trade_id>`
  - 移动止损:`boss-trail-<trade_id>`
  - 信号失效平仓:`boss-sigfail-<trade_id>`
  - 超时平仓:`boss-timeout-<trade_id>`
  - 手动平仓:`boss-manual-<trade_id>`

### 特殊错误处理(必读)
- `-1006 / -1007 UNKNOWN/TIMEOUT`:**API 可能已成功**,必须查询订单状态确认
- `-2011 / -2013`:订单不存在,**当成已撤销/已成交**
- `-4046 / -4059`:幂等错误,**当成成功**
- `-2014 / -2015 / -1022`:**致命**,进程级告警 + 暂停所有 API
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
