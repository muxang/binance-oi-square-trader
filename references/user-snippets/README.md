# 用户提供的代码片段 — 业务逻辑锚点

> 本目录下的代码是**用户已经验证过的参考实现**。
> 本项目用 Go 重写,但**核心业务逻辑(阈值、判定条件、计算公式)必须与原片段保持一致**。

## 文件清单

| 文件 | 来源 | 锚定的业务逻辑 |
|---|---|---|
| `contract-monitor.js` | 用户原 Node.js 代码 | **OI 暴涨判定**、大户持仓比、市值占比检测 |
| `square-discussion.py` | 用户原 Python 代码 | Square `queryByHashtag` 调用方式 |

---

## OI 暴涨判定(强制锚定)

### 唯一来源

`contract-monitor.js` 中的 `checkOpenInterestSurge` 方法。

Claude Code 实现 OI 信号时:
1. **不要自由发挥**,不要凭"我觉得这样判断更合理"修改逻辑
2. **算法、阈值、回溯周期必须 1:1 还原** Go 版本
3. 配置文件里的可调参数,默认值必须等于 JS 代码里的取值

### 必须保留的判定逻辑

来自 `contract-monitor.js` 的 `checkOpenInterestSurge`:

```
输入: openInterestStats.openInterestData[]  (5min 周期 OI 时间序列)

变量:
  data = openInterestData
  currentValue = data[last]
  lookbackPeriods = min(10, data.length)

步骤 1: 找最近 lookbackPeriods 周期中的最低值
  minValue = min(data[-lookbackPeriods:])
  minIndex = argmin(...)

步骤 2: 计算从最低点到当前的增长幅度
  growthFromMin = (currentValue - minValue) / minValue

步骤 3: 计算最近 6 个周期的总体增长
  recentPeriods = min(6, data.length)
  recentStartValue = data[-recentPeriods]
  recentGrowth = (currentValue - recentStartValue) / recentStartValue

步骤 4: 统计最近周期中相邻递增次数
  growingPeriods = count(data[i] > data[i-1] for i in [-recentPeriods+1 .. last])

步骤 5: 触发条件(全部满足)
  isAlert = (
    growthFromMin >= config.minSurgePercentage         // 默认 0.05 (5%)
    AND recentGrowth >= 0.03                            // 硬编码 3%
    AND growingPeriods >= floor(recentPeriods / 2)      // 至少一半周期在涨
  )
```

### 配置项映射(Go 配置 → JS 原值)

| Go 配置项 | 默认值 | 对应 JS 取值 |
|---|---|---|
| `OI_SURGE_FROM_LOW_PCT` | `0.05` | `config.monitoring.minSurgePercentage` |
| `OI_SURGE_RECENT_GROWTH_PCT` | `0.03` | 硬编码 `0.03` |
| `OI_SURGE_LOOKBACK_PERIODS` | `10` | 硬编码 `10` |
| `OI_SURGE_RECENT_PERIODS` | `6` | 硬编码 `6` |
| `OI_SURGE_MIN_GROWING_RATIO` | `0.5` | `Math.floor(recentPeriods / 2)` 等价 |

### 不接顶保护(SPEC v0.3 加的额外条件)

JS 原版没有,但 SPEC 要求:
```
AND currentPrice > price_60min_ago
```
这条来自 SPEC 而非 JS 片段,Go 实现时单独加,注释里说明 "SPEC 追加,JS 原版无"。

### Go 实现要求

```go
// ref: references/user-snippets/contract-monitor.js (checkOpenInterestSurge)
// 算法、阈值、回溯周期与原 JS 实现 1:1 一致。
// 不接顶保护(currentPrice > price_60min_ago)是 SPEC v0.3 追加,JS 原版无。

func evaluateOISurge(data []OIPoint, currentPrice, price60mAgo float64, cfg OISurgeConfig) OISurgeSignal {
    // ... 严格还原 JS 逻辑
}
```

---

## 其它复用逻辑(本项目暂不入决策,但代码保留)

`contract-monitor.js` 中以下逻辑**不进 v0.1 决策路径**(SPEC 明确),但 Go 数据采集层应保留对应数据,供未来 v0.2 使用:

- `checkLargeHolderRatioWithData`:大户账户/持仓多空比 < 阈值告警
- `checkMarketCapRatio`:持仓市值 / 流通市值 ≥ 50% 告警
- `priceTrend`、`smartMoneyData`、`spotCapitalFlow`

实现策略:**采集 + 落库,但不进信号决策**。这样 v0.2 升级时不需要重新拉历史数据。

---

## Square Hashtag 查询(强制锚定)

### 唯一来源

`square-discussion.py` 中的 `get_square_discussion(coin)` 函数。

```python
# 原代码摘要(以 user-snippets/square-discussion.py 为准):
url = "https://www.binance.com/bapi/composite/v4/friendly/pgc/content/queryByHashtag"
params = {
  "hashtag":   f"#{coin.lower()}",
  "pageIndex": 1,
  "pageSize":  1,
  "orderBy":   "HOT"
}
headers = {
  "User-Agent": "Mozilla/5.0",
  "Referer":   "https://www.binance.com/en/square"
}
# 返回: data.hashtag.contentCount, data.hashtag.viewCount
```

### Go 实现要求

- URL、params、headers 字段名必须**完全一致**
- 返回字段路径 `data.hashtag.contentCount` / `data.hashtag.viewCount` 不变
- 对失败/超时的处理:**返回 0, 0**,不抛异常(符合 Python 原版 `try/except` + `return 0, 0` 行为)

```go
// ref: references/user-snippets/square-discussion.py (get_square_discussion)
// URL、headers、参数、错误处理与原 Python 实现一致。

func (c *SquareClient) GetHashtagStats(ctx context.Context, coin string) (contentCount, viewCount int64, err error) {
    // ... 严格还原 Python 逻辑
    // 失败时返回 (0, 0, nil),不让上层判断 err
}
```

---

## 不允许的"自由发挥"

Claude Code 实现这两块逻辑时,**禁止**:

- ❌ "我觉得用 EMA 平滑比直接看 min 更好"
- ❌ "我加一个边界保护防止除零"(除非原代码已有)
- ❌ "我把阈值写成动态自适应"
- ❌ "我把 60 改成 50,这样信号更多"
- ❌ 任何**未在 SPEC 明文列出**的算法变更

如果 Claude Code 认为某处可以改进,**写在 review 里建议**,**不要写进代码**。
