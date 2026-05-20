# CoinGecko — Circulating Supply 数据源

> mu 批准 (2026-05-20, R.11.A 决策 A1) 引入 CoinGecko Demo (free) API,
> 仅用于获取 watchlist symbols 的 `circulating_supply`,以计算合约持仓
> 市值占比 (`marketCapRatio = openInterest × price / (circulating_supply × price)`)。
>
> 仅用 Demo 免费档,**绝不**升级到 Pro 付费档。

## 唯一登记 URL

| 名称 | URL |
|---|---|
| Coins Markets (含 circulating_supply) | https://docs.coingecko.com/reference/coins-markets |
| Coins List (symbol → id 映射) | https://docs.coingecko.com/reference/coins-list |
| Demo 鉴权说明 | https://docs.coingecko.com/reference/setting-up-your-api-key |
| Rate Limit 说明 | https://docs.coingecko.com/reference/common-errors-rate-limit |

## 关键参数(摘自 web_fetch 2026-05-20)

### Base URL
- **Demo (Free)**: `https://api.coingecko.com/api/v3/`
- ❌ Pro: `https://pro-api.coingecko.com/api/v3/`(本项目不用)

### 鉴权
- Demo 用 query param: `?x_cg_demo_api_key=<KEY>`
- 不要写入 yaml,走环境变量 `COINGECKO_DEMO_API_KEY`

### 速率限制
- Demo: **~30 calls/min**(随流量浮动)
- 月配额:文档未明示
- **本项目设计目标**: ≤1 call/min 平均(6h cron × 2 batch = 8 calls/day)

### 端点 `/coins/markets`
```
GET /coins/markets
  vs_currency=usd   (必填)
  ids=bitcoin,ethereum,...  (≤250 个 id, 推荐用 ids 而非 symbols 避免歧义)
  per_page=250
  page=1

response: [{
  id: "bitcoin",
  symbol: "btc",
  current_price: 60000,
  market_cap: 1200000000000,
  circulating_supply: 19700000.0,
  ...
}]
```

### 端点 `/coins/list`
```
GET /coins/list
  include_platform=false

response: [{ id: "bitcoin", symbol: "btc", name: "Bitcoin" }, ...]
```
启动 1 次拉取,缓存 (id, symbol) mapping。

## 设计约束

1. **频率**:每 6 小时拉一次,283 symbol 分 ≤2 个 batch
2. **失败容忍**:CoinGecko 故障**不**阻塞主流程(market_cap_ratio 字段写 NULL)
3. **Symbol 映射歧义**:同 symbol 多 id 时取 market_cap 最大那个
4. **Binance futures 后缀去除**:`BTCUSDT` → `BTC` 再去 CoinGecko 查
5. **代码注释强制引用** `references/external/coingecko.md`

## 不做的事

- ❌ 不用 CoinGecko 价格(用币安 latest price 一致)
- ❌ 不用 CoinGecko 24h 数据(用币安 ticker)
- ❌ 不接 Pro 付费档
