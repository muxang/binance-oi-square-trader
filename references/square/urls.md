# Binance Square 接口来源(非官方)

> Square API 没有官方文档。本项目所有 Square 接口的**事实标准**来自下面两个来源。
> Claude Code 涉及 Square 接口的代码,**必须**以这些来源为唯一依据。

---

## 主要来源(GitHub)

### skingchan/Binance-Square-Analysis ⭐

仓库:https://github.com/skingchan/Binance-Square-Analysis

**这是本项目 Square 接口调用方式的 source of truth。**

| 文件 | URL | 用途 |
|---|---|---|
| README.md(项目说明、接口分析) | https://github.com/skingchan/Binance-Square-Analysis/blob/main/README.md |  接口背景、分页机制、字段说明 |
| fetch_data.ps1(推荐流抓取) | https://github.com/skingchan/Binance-Square-Analysis/blob/main/fetch_data.ps1 | `feed-recommend/list` 调用范本 |
| binance_square.py | https://github.com/skingchan/Binance-Square-Analysis/blob/main/binance_square.py | Python 调用范本 |
| server.js | https://github.com/skingchan/Binance-Square-Analysis/blob/main/server.js | 提取 cashtag、情绪分析逻辑 |
| analyze_sentiment.ps1 | https://github.com/skingchan/Binance-Square-Analysis/blob/main/analyze_sentiment.ps1 | 情感分析关键词表 |

**Raw 文件**(便于 web_fetch 拿原文):
- https://raw.githubusercontent.com/skingchan/Binance-Square-Analysis/main/README.md
- https://raw.githubusercontent.com/skingchan/Binance-Square-Analysis/main/fetch_data.ps1
- https://raw.githubusercontent.com/skingchan/Binance-Square-Analysis/main/server.js

> Claude Code 实现前用 `web_fetch` 拉这几个 raw URL,理解清楚再写。

---

## 用户提供的代码片段

### Square Hashtag 查询 — `queryByHashtag`

来源:`references/user-snippets/square-discussion.py`(用户原始代码)

接口:`GET /bapi/composite/v4/friendly/pgc/content/queryByHashtag`

> 这是**单 hashtag 查询**接口,与上面 GitHub 项目的 `feed-recommend` 是**两个不同接口**:
> - `feed-recommend`(本项目用于"发现"哪些币种被讨论)
> - `queryByHashtag`(本项目用于"跟踪"已知币种的热度时序)
>
> Go 实现时,Square 客户端必须同时支持这两个接口。

---

## 业务接口分工(本项目)

| 用途 | 接口 | 来源 |
|---|---|---|
| **发现热议币种**(每 1h) | `POST /bapi/composite/v9/friendly/pgc/feed/feed-recommend/list` | github skingchan |
| **跟踪单币种热度时序**(每 5min) | `GET /bapi/composite/v4/friendly/pgc/content/queryByHashtag` | user-snippets |

---

## 通用调用约束(强制)

### 请求头(从 GitHub 项目摘录)

```
Content-Type:   application/json
User-Agent:     Mozilla/5.0
Bnc-Uuid:       <UUID v4, 启动时生成>
Clienttype:     web
Origin:         https://www.binance.com
Referer:        https://www.binance.com/zh-CN/square
Cookie:         bnc-uid=<同 Bnc-Uuid>; lang=zh-CN
```

> Bnc-Uuid 是匿名标识,本项目**自己生成 UUID v4**,不复用 GitHub 项目里的固定值,降低被批量限流的风险。

### 解析必须宽松

非官方接口,字段名/层级可能变动:
- 用 `gjson` 或类似宽松解析,不要强类型反序列化所有字段
- 关键字段缺失时,记 warn 日志,不让进程挂掉
- 接口返回失败(http 4xx/5xx)时,跳过本轮采集,**不**重试到死

### 限流保护

非官方接口未公开限流。本项目自我约束:
- `feed-recommend` ≤ 8 次/小时(分页轮询)
- `queryByHashtag` ≤ 60 个币种/5 分钟,并发 ≤ 5
- 失败连续 3 次以上 → 暂停 30min + TG 告警

---

## 引用样例

```go
// ref: references/square/urls.md §「skingchan/Binance-Square-Analysis」
// source: https://github.com/skingchan/Binance-Square-Analysis/blob/main/fetch_data.ps1
// 实现 feed-recommend 推荐流抓取,分页机制必须与原项目一致(contentIds 去重)。

// ref: references/user-snippets/square-discussion.py
// 实现 queryByHashtag, 阈值/字段名以原 Python 代码为准。
```
