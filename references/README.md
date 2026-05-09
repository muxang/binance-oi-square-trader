# References — 唯一信息源

本目录是项目所有外部 API、协议、第三方代码的**唯一信息源索引**。
任何代码涉及外部接口或参考别人代码,**必须**追溯到本目录登记的来源。

## 使用规则(对 Claude Code,强制)

### 1. 调外部 API 前必读官方文档

涉及币安 / Square / 第三方接口时,**必须先 web_fetch 对应的官方 URL** 拿到当前文档,再写代码。
**严禁**凭训练数据里的记忆写接口调用 — 模型记忆可能过时,真实接口可能已变更。

### 2. URL 白名单制

只能从下列文件中登记的 URL 取信息:
- `binance/urls.md`         — 币安官方文档 URL 索引
- `square/urls.md`          — Square 非官方接口来源索引
- `github/urls.md`          — 第三方 GitHub 项目索引

如果你需要的信息不在以上索引中:
1. **停下来**,不要 web_search 然后随意采信结果
2. 列出"需要 X 信息但 references 索引里没有"
3. 让用户决定:补充索引 / 跳过这部分 / 给出权威来源
4. 用户明确批准后,才能从批准的 URL 拿信息

### 3. 代码必须留引用注释

每处涉及外部接口的代码,注释里必须给出 references 来源:

```go
// ref: references/binance/urls.md §「Open Interest Statistics」
// docs: https://developers.binance.com/.../Open-Interest-Statistics

// ref: references/user-snippets/contract-monitor.js (checkOpenInterestSurge 逻辑)
```

### 4. 用户提供的代码片段单独处理

`user-snippets/` 下是用户给的参考实现,规则:
- 这是**逻辑参考**,不是可直接复制的代码
- 本项目用 Go 重写,逻辑保持但实现是 Go 风格
- 阈值、常数、判定条件必须**完全一致**,除非 SPEC 明确不一致

### 5. 不要自己往本目录加文件

需要补充时告诉用户,由用户决定加什么。

---

## 关键时间节点(代码必须感知)

> **2025-12-09**:币安 USDⓈ-M 永续条件单(STOP_MARKET / TAKE_PROFIT_MARKET 等)
> 迁移到 Algo Service 新接口。本项目灾难止损依赖 STOP_MARKET,**必须**实现日期感知的接口切换。
> 详情自查:https://developers.binance.com/docs/derivatives/change-log

## 目录索引

```
references/
├── README.md                   ← 本文件(使用规则)
├── binance/
│   └── urls.md                 ← 币安官方文档 URL 索引
├── square/
│   └── urls.md                 ← Square 非官方接口来源
├── github/
│   └── urls.md                 ← 第三方 GitHub 项目
└── user-snippets/
    ├── README.md               ← 片段使用说明
    ├── contract-monitor.js     ← 用户原代码: OI/大户监控
    └── square-discussion.py    ← 用户原代码: Square hashtag 查询
```
