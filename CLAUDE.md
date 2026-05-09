# CLAUDE.md — Claude Code 工作守则

> 本文件是 Claude Code 在本仓库工作的**最高优先级守则**,优先级高于一切默认行为。
> 与 SPEC.md / ARCHITECTURE.md 冲突时,**停下问用户**,不自行裁决。

---

## 项目背景

币安 USDⓈ-M 永续合约自动化交易系统(仅做多)。
真实资金 1000 USDT 实盘运行。
**任何代码缺陷都可能直接造成经济损失。**

---

## 必读文档(每次会话开始 view 一次)

```
SPEC.md            业务需求规格 ─── "做什么"
ARCHITECTURE.md    技术架构    ─── "用什么、怎么部署"
CLAUDE.md          本文件      ─── "怎么做"
references/        外部信息源  ─── 详见 references/README.md
```

---

## 核心工作原则

### 1. 严格按 Phase 推进

```
Phase 0 → 1 → 2 → 3 → 4 → 5 → 6
```

不跳阶段。每个 Phase 完成必须停下,输出:

- 完成项 vs Acceptance Criteria 对照表
- 已知缺陷或 TODO
- 给用户的 review 重点(3-5 条)
- **明确询问是否进入下一 Phase**

未得到明确同意,不进入下一 Phase。

### 2. 涉及钱的逻辑零容忍

以下文件每处改动必须有单元测试 + 边界用例:

- `internal/decision/sizing.go`
- `internal/execution/trader.go`
- `internal/execution/exit.go`
- `internal/position/manager.go`
- `internal/risk/circuit_breaker.go`

写这些模块遇到任何不确定,**停下来问用户**,不要假设。

### 3. 每写完一个模块停下来 review

每完成一个文件或一组紧密相关的文件:
- 列出新增/修改的文件
- 说明关键决策
- 给出测试方法
- **等用户确认后再继续**

不要一口气写 10 个文件然后让用户翻 5000 行。

### 4. 严禁假数据 / 偷懒 mock

- DB 测试用 dockertest 起真 PG/Redis
- 币安 API 测试连 testnet
- 不在关键交易路径用 mock 替换真实调用

如果某测试必须用 mock,在文件顶部加注释说明,并在 review 中明确告知用户。

### 5. 遇到不确定就问

以下情形必须停下问用户:
- SPEC / ARCHITECTURE 未明确的边界条件
- 币安 API 字段含义不确定 → 先 web_fetch 官方文档,仍不确定再问
- 错误处理策略不明
- 涉及金额/精度/杠杆的计算
- 任何"可能这样也行"的时刻

---

## 参考资料约束(强制)

### 6. references/ 是唯一信息源

涉及外部 API、第三方代码、协议格式时:

- ✅ 必须从 `references/` 索引中找对应来源
- ✅ 必须 `web_fetch` 索引中登记的 URL 拿到当前文档
- ❌ 严禁凭训练数据里的记忆写币安/Square 接口
- ❌ 严禁参考索引外的 GitHub 项目或博客
- ❌ 严禁随便 web_search 然后采信不在索引里的来源

如果索引里没有所需信息:
1. 停下来,不要瞎猜
2. 列出"我需要 X 信息但 references 索引里没有"
3. 让用户决定:补充索引 / 跳过 / 给权威来源
4. **绝不**:基于记忆补全、推测补全、用 search 结果直接补全

### 7. 引用必须可追溯

每处涉及外部接口的代码,注释里必须给出 references 来源:

```go
// ref: references/binance/urls.md §「Open Interest Statistics」
// docs: https://developers.binance.com/.../Open-Interest-Statistics
// fetched: 2026-05-09

// ref: references/user-snippets/contract-monitor.js (checkOpenInterestSurge)
// 算法、阈值、回溯周期与原 JS 实现 1:1 一致
```

### 8. 用户片段是逻辑锚点

`references/user-snippets/` 下的代码是用户**已验证**的实现:
- **OI 暴涨判定**算法、阈值、周期 — 与 `contract-monitor.js` 1:1 一致
- **Square hashtag 查询** — 与 `square-discussion.py` 1:1 一致
- 配置默认值 = JS/Python 原值
- 不要"自由优化",有想法写到 review 里建议

---

## 最小实现原则(强制)

### 9. 写最少的代码完成需求

- 只实现 SPEC + 当前 Phase Acceptance Criteria 明确要求的
- ❌ 不"顺手优化"、不"未来用得到的抽象"、不"工业标准应该有的"额外模块
- 想加的功能,**写在 review 里建议**,不写进代码

### 10. 变更预算

每次响应:
- 最多新增/修改 **3 个文件**
- 单文件单次改动 **不超过 200 行**
- 超出预算 → 拆多次,每次提交后等 review

例外:首次创建项目骨架(go.mod、Makefile、目录占位)可一次性完成。

### 11. 拒绝 scope creep

用户在某 Phase 中途要求做 Phase 之外的事:
1. 礼貌提醒超出当前 Phase
2. 询问:本 Phase 临时插入 / 留到对应 Phase / 加 TODO
3. 等用户决定

### 12. 文件长度上限

- 单 Go 文件超过 400 行 → 停下询问是否拆分
- 单函数超过 80 行 → 停下询问是否重构

---

## 实现深度约束(强制)

### 13. 默认实现"够用版"

- 重试默认 3 次,指数退避起点 1s — 不写自适应
- 错误处理默认 log + 上抛 — 不写复杂恢复
- 速率限制默认 token bucket — 不写动态调整
- 缓存默认固定 TTL — 不写 LRU/LFU/W-TinyLFU

要更复杂的实现,在 review 中提议,让用户决定。

### 14. 不写"将来可能有用"的接口

- 接口只为当前实现 + 当前测试存在
- 单实现的抽象 = 不抽象,直接结构体
- 没要求多策略/多交易所 = 不做对应抽象

### 15. 不写无用的防御代码

- 不为永远不会发生的情况写 if 分支
- 不在不可能为 nil 的地方加 nil 检查
- **跨网络/跨进程边界除外**(必须严防)

---

## Go 编码规范

### 16. 基础

- Go 1.25+,启用 `-race` 跑测试
- `golangci-lint` 必须通过
- 错误:永不丢 error,自定义 error 用 `fmt.Errorf("...: %w", err)` 包裹
- panic 仅用于"绝不应发生"的不变式,正常错误一律返回 error

### 17. 并发

- 所有跨网络调用第一个参数必须是 `ctx`
- cron 任务必须能被 ctx cancel
- 严禁裸 `go func()`,必须用 errgroup 或 wait group
- long-running goroutine 必须 defer recover + 日志 + 告警

### 18. 接口

- 在使用方定义接口,不在实现方
- 接口 ≤ 3 个方法

### 19. 数据库

- schema 改动通过 `golang-migrate` 迁移文件,不直接改库
- 查询用 sqlc 生成,不手写 db.Query
- 时序表用 TimescaleDB hypertable
- **金额列用 `numeric(36, 18)`**,严禁 `float`

### 20. 币安 API

- 所有调用走 `internal/binance` 包
- 业务代码不直接构造 HTTP 请求或拼 URL
- 必须实现速率限制(请求权重计数)
- 错误分类按 `references/binance/urls.md` 中"特殊错误处理"章节执行

### 21. 日志(zerolog)

- `Info` 关键业务事件:信号、入场、平仓、熔断触发
- `Warn` 异常但已处理
- `Error` 需要关注的错误
- `Fatal` 进程必须退出
- 关键交易事件必须带:`symbol`、`trade_id`、`signal_id`(如适用)

### 22. 配置

- 全部走 `internal/config`
- 不在代码硬编码任何阈值
- 敏感信息(API key/TG token)走环境变量,不进 yaml

---

## Git / Commit 规范

- 分支:`main` 受保护,功能分支 `phase-N/feature-name`
- 每个 Phase 一个 git tag(`phase-0`,`phase-1`,...)
- Commit 格式:`<type>(<scope>): <subject>`
  - type: `feat`/`fix`/`refactor`/`test`/`docs`/`chore`
- 每个 commit 必须可独立编译通过 + 测试通过
- **严禁**:在 git 提交里包含 API key、私钥、敏感信息

---

## 实盘安全闸(强制)

代码区分两种运行模式:

```
TRADER_MODE=testnet      读接口走主网拿真实数据, 写接口走 testnet 测试下单
TRADER_MODE=mainnet      连币安主网, 真实下单
```

约束:
- **默认值必须是 `testnet`**,绝不是 mainnet
- 切到 `mainnet` 必须在启动日志 ⚠️ 高亮 5 行
- 切到 `mainnet` 必须读环境变量 `TRADER_MAINNET_CONFIRM=I_UNDERSTAND`,
  否则进程立即退出 + sleep 5s 给操作员反应时间
- 测试代码**严禁**配置 `TRADER_MODE=mainnet`,CI 必须扫这点
- BinanceClient 在 testnet 模式下,**写请求(POST/DELETE)硬绑 testnet base URL**,
  即使 bug 也不会误打主网

---

## 时区铁律(强制)

详见 `ARCHITECTURE.md §9.6`。摘要:

- DB 一律 `TIMESTAMPTZ`,内部一律 UTC
- **严禁** `time.Now()` 裸用,统一 `timez.NowUTC()`
- 仅在 cron / TG 渲染 / Dashboard 展示用 BJT
- Cron 必须 `cron.WithLocation(timez.BJT)` 显式指定
- CI 守卫:`grep time.Now()` 扫业务代码

---

## Proxy 约束(强制)

详见 `ARCHITECTURE.md §9.5`。摘要:

- 业务代码**严禁**直接构造 HTTP 客户端
- 必须从 `internal/binance` 拿 `Client`,代理由 `Client` 内部管理
- WS 连接(User Data Stream)同样必须走 ProxyManager
- 代理失败**不**自动 fallback 直连
- 代理认证信息**不**进日志

---

## 沟通约束

### 简短模式

- 完成一个文件 → 一句话总结 + 文件路径
- 不解释 Go 语法基础
- 不解释"为什么没做某件事"(除非用户问)
- 不在每个响应末尾问"还需要做什么吗",直接停下等指令

### Diff 优先

- 修改已有文件 → **只展示 diff**
- 新建文件 → 展示完整内容,**不超过 200 行**
- 超过的部分用 `// ... 省略` 标注,告诉用户"完整代码已写入 X 路径"

### Phase 进度报告(统一格式)

```
Phase N 进度
├── ✅ 已完成: [清单]
├── 🟡 进行中: [清单]
├── ⚪ 未开始: [清单]
├── ❓ 阻塞:   [需要用户决策的事项]
└── 📋 下一步: [具体动作]
```

---

## 你不能做的事

- ❌ 跳过 Phase 顺序
- ❌ 不写测试就交付涉及钱的模块
- ❌ 修改 SPEC.md / ARCHITECTURE.md / CLAUDE.md 而不询问用户
- ❌ 在交易代码里加未在 SPEC 中描述的"小优化"
- ❌ 用 mock 代替真实 API/DB 来"通过"测试
- ❌ 在 git 提交包含 API key / 私钥 / 敏感信息
- ❌ 直接执行币安主网下单(开发期一律 testnet)
- ❌ 用 float64 表示金额
- ❌ 一次响应改动超过 3 个文件 / 超过 200 行
- ❌ 凭记忆写币安/Square API,不 web_fetch 官方文档
- ❌ 引用 references 索引外的 URL 或代码

---

## 你应该做的事

- ✅ 经常 view SPEC / ARCHITECTURE 校对方向
- ✅ 写代码前 web_fetch references 索引中对应的官方文档
- ✅ 写代码前先描述思路,得到确认再动手
- ✅ 每个模块完成后给出测试方法和 review 重点
- ✅ 主动用 todo list 跟踪 Phase 进度
- ✅ 看到现有代码有问题主动指出,但不擅自重构
- ✅ 主动提出 SPEC 里没考虑的边界情况

---

## 当前 Phase

> 由用户在每次会话开始时告知。如果未告知,**主动询问**。

---

## 紧急情况

如果发现已合入代码可能导致资金风险:

1. 立即停止当前任务
2. 在响应顶部用 🚨 高亮告警
3. 描述风险 + 复现路径 + 建议修复
4. 等用户决定

不要自己默默修复了事。
