# 第三方 GitHub 项目索引

> 本项目可参考的 GitHub 来源。**未列在此处的仓库不能作为信息源。**

---

## skingchan/Binance-Square-Analysis ⭐

> Square 接口调用的事实标准。详见 `references/square/urls.md`。

仓库:https://github.com/skingchan/Binance-Square-Analysis

| 文件 | Raw URL | 关键内容 |
|---|---|---|
| README.md | https://raw.githubusercontent.com/skingchan/Binance-Square-Analysis/main/README.md | 接口分析、分页机制 |
| fetch_data.ps1 | https://raw.githubusercontent.com/skingchan/Binance-Square-Analysis/main/fetch_data.ps1 | `feed-recommend` 调用范本 |
| server.js | https://raw.githubusercontent.com/skingchan/Binance-Square-Analysis/main/server.js | cashtag 提取 + 情绪分析 |
| analyze_sentiment.ps1 | https://raw.githubusercontent.com/skingchan/Binance-Square-Analysis/main/analyze_sentiment.ps1 | 情绪关键词表 |

---

## (预留)Go 币安 SDK 参考

如果实现币安客户端时需要参考成熟 SDK 的写法(签名、错误处理):

| 项目 | URL | 用途 |
|---|---|---|
| adshao/go-binance | https://github.com/adshao/go-binance | 仅作 reference,本项目自封装 HTTP 客户端 |

> **本项目不直接依赖此 SDK**,因为它对 USDⓈ-M 期货的覆盖经常滞后。
> 仅在签名算法、参数序列化等基础点不确定时,作为参考。

---

## 引用约束

- 仅在本文件登记的 GitHub 仓库可作为代码参考来源
- 引用必须给到具体文件 + raw URL,不能只给仓库链接
- "我看到 GitHub 上某项目这么写"——**这种话 Claude Code 不能说**,要么登记到本文件,要么不参考
