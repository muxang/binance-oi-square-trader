# Claude Code 开场白(直接复制粘贴使用)

> 把下面这段话作为你与 Claude Code 的**第一条消息**。
> 这一步至关重要 — 第一句怎么说,决定它后续会不会按你的约束工作。

---

## 开场白(中文版)

```
我要开发一个币安 USDⓈ-M 永续合约自动化交易系统。这是一个 1000 USDT 实盘项目,任何代码缺陷都会直接造成经济损失。

仓库根目录的三份文档是必读三件套:
- SPEC.md         业务需求规格(做什么)
- ARCHITECTURE.md 技术架构(用什么、怎么部署)
- CLAUDE.md       你的工作守则(怎么做)

references/ 目录是外部信息源唯一索引:
- references/binance/urls.md   币安官方文档 URL 列表
- references/square/urls.md    Square 非官方接口来源(锚定 GitHub 项目)
- references/github/urls.md    第三方 GitHub 索引
- references/user-snippets/    用户已验证的参考代码(OI 算法、Square 调用)

请你现在:
1. view SPEC.md / ARCHITECTURE.md / CLAUDE.md / references/README.md / references/user-snippets/README.md
2. 列出 CLAUDE.md 里你认为最重要的 5 条约束(确认你读懂了)
3. 列出 Phase 0 的工作清单(对照 SPEC.md 的 Phase 0 Acceptance)
4. 等我确认后再动手

注意:
- 严格按 Phase 0 → 1 → 2 → ... 顺序,不跳阶段
- 写代码前 web_fetch references/binance/urls.md 中对应的官方文档拿当前内容
- 严禁凭训练数据里的记忆写币安 / Square API
- 单次响应不超过 3 个文件 / 200 行
- 涉及金额的计算用 decimal,严禁 float
- 默认 TRADER_MODE=testnet,绝不直接连主网
- 时区铁律:DB/内部 UTC,显示/日界 BJT,严禁 time.Now() 裸用
- 批量采集币安默认有 proxy 抽象层,业务代码不直接构造 HTTP client
- 遇到任何不确定停下来问我

OI 暴涨判定算法必须 1:1 锚定 references/user-snippets/contract-monitor.js,
不要自由发挥。Square 接口调用方式锚定 references/user-snippets/square-discussion.py
和 references/square/urls.md 中登记的 GitHub 项目。

确认理解后开始 Phase 0。
```

---

## 关键提醒

- 给 Claude Code 的**每个新会话开始**都要让它 view 三件套 + references/README.md
- 每个 Phase 切换前再次确认 CLAUDE.md 的约束
- 它如果开始"自由发挥",立刻打断,引用 CLAUDE.md 的具体条款
