# NOFX 复制交易功能变更速记

面向同事的快速总览，涵盖后端/前端新增逻辑、接口与注意事项，便于接手与回归测试。

## 后端

- **信号源与交易员配置**
  - Trader 支持信号源类型：`ai`（默认）、`hyperliquid_wallet`、`okx_wallet`。
  - 接口 `/api/my-traders`、`/api/traders/:id/config` 返回 `signal_source_type`、`signal_source_value`、`copy_trading_config`。
  - 更新接口 `/api/traders/:id` 若未传 `signal_source_type`，保留原值，不再强制重置为 `ai`。
  - 复制模式下要求提供 `signal_source_value`，否则 400。

- **复制交易 sizing 逻辑**
  - 开/加仓：按照“领航员保证金占其净值的比例”同步到跟随者，并乘以跟单系数：
    ```
    leaderMargin = leaderNotional / leaderLeverage
    proportion   = leaderMargin / leaderEquity
    followerMargin = proportion * followerEquity * (follow_ratio/100)
    ```
    再套用最小/最大成交额（min/max_amount），不足最小额会强制提升；超过最大额截断。
  - 减/平仓：按领航员本次变动占其持仓比例作用到本地持仓；全平直接平掉本地当前仓位。
  - 方向翻转：先 close 再 open 反向。

- **行情与价源兜底**
  - 领航员信号缺成交价时，优先取 fills；无价则用内部行情模块的最新/标记价；仍无则跳过本次，不更新 lastPositions，避免丢首笔信号。

- **持仓差分与全平同步**
  - OKX/Hyperliquid provider 按当前持仓与快照差分生成 add/reduce/close。
  - OKX 额外对比快照差集，对已消失的符号发 close_long/close_short，并清理缓存，防止领航员全平后跟单端残留。

- **决策日志增强**
  - `DecisionAction` 新增记录：`leader_equity/notional/margin/price`、`follower_equity/margin`、`copy_ratio`、最小/最大额是否触发。
  - 方便前端展示“领航员→跟随者”换算过程。

- **接口补充**
  - `/api/my-traders`：返回 `exchange_id`、`initial_balance`、信号源与 copy 配置。
  - `/api/traders/:id/config`：同上，供编辑弹窗使用。

## 前端

- **TraderDashboard**
  - 价格/数量格式化展示（千位少位数：≥1000 保留 1 位，≥10 保留 2 位，其他 4 位）。
  - 持仓表新增“保证金”列，收益/方向颜色标识。
  - 执行日志卡片：成功/失败底色，展示执行动作、数量、价格等。

- **最近决策卡片**
  - 显示交易周期号、时间、执行结果，展开输入提示与 CoT。
  - 展示执行日志与账户状态摘要。

- **交易员配置（TraderConfigModal）**
  - 支持选择信号源类型（AI / Hyperliquid 钱包 / OKX 钱包）及钱包/交易员 ID。
  - 复制配置：跟随开/加/减、同步杠杆/保证金模式、跟单系数、最小/最大成交额。
  - 编辑时从 `/api/traders/:id/config` 读取，保留原有信号源，不再被默认成 AI。

- **类型定义**
  - `TraderInfo/TraderConfigData/CreateTraderRequest` 等新增 `signal_source_type/value`、`copy_trading_config` 字段。

## 运维与测试要点

- 部署后确认接口返回字段：
  ```bash
  curl -H "Authorization: Bearer <token>" http://127.0.0.1:8080/api/my-traders
  curl -H "Authorization: Bearer <token>" http://127.0.0.1:8080/api/traders/<trader_id>/config
  ```
  应包含 `signal_source_type/value` 与 `copy_trading_config`。
- 复制模式小额账户注意交易所最小下单额（如 $10）；必要时提高“最小成交额”配置或跟单系数。
- 方向翻转/全平场景需回归：OKX 差集 close 是否下发，Hyperliquid 价源兜底是否避免漏首笔。

## 变更范围（文件）

- 后端：`copytrading/*.go`、`trader/auto_trader.go`、`logger/decision_logger.go`、`api/server.go`、`config/database.go`
- 前端：`web/src/pages/TraderDashboard.tsx`、`web/src/components/TraderConfigModal.tsx`、`web/src/types.ts` 及相关类型/视图更新。

## 目标与预期达成效果（项目层面）

- **核心目标**：把 NOFX 从单机 AI 决策扩展为“可跟随领航员/钱包的定比复制交易平台”，确保盈亏曲线同步、风控透明，并降低因价源/最小单限制导致的漏单。
- **要达成的目的**
  - 支持多信号源：AI、本地 Hyperliquid/OKX 钱包；用户可切换，不丢配置。
  - 定比复制：按“领航员保证金占净值比例 × 跟单系数”复制开/加仓，按领航员持仓变动比例复制减/平仓；支持方向翻转先平后开。
  - 风控阈值：最小/最大成交额，防止过小/过大下单；同步杠杆/保证金模式可选。
  - 价源兜底：fills 缺价时用行情模块价格，避免漏掉首笔信号。
  - 全平同步：领航员全平或合约消失时，强制下发 close 并清快照，避免残留仓位。
  - 可观测性：决策日志携带领航员与跟随者的保证金/净值/价格/最小单触发等信息，前端可直观看到换算过程与失败原因。
  - 前端体验：持仓表格式化、保证金列；决策卡片展示执行细节、错误信息；交易员配置支持复制模式与阈值设置。

## 后续可考虑的功能

- 交易所最小名义额度的内置下限：开仓金额取 `max(用户最小额, 交易所最小名义)`，减少被拒单。
- 币种粒度的最小/最大额与跟单系数配置，适配高价/低价币。
- 复制开仓的可视化提示：在前端提示“按比例后低于交易所最小单，已抬升到 X USDT”。
- 更细的运行态监控：复制失败率、最小单触发次数、价源兜底次数。 

## 领航员信号监听与接口概览

- Hyperliquid 钱包
  - 配置项：`signal_source_type=hyperliquid_wallet`，`signal_source_value=<walletAddr>`。
  - 监听：轮询/订阅 fills/positions，缺价时 fallback 到行情模块；差分当前持仓与快照生成 add/reduce/close。
  - 价源：fills 价格优先，缺价用 `market.Get(symbol)` 最新/标记价。
- OKX 钱包
  - 配置项：`signal_source_type=okx_wallet`，`signal_source_value=<uniqueName/wallet>`。
  - 监听：轮询 positions，差分生成信号；遍历完当前持仓后对快照差集发 close，清理缓存，保证全平同步。
  - 价源：fills/行情兜底同上。
- 通用信号字段（传入复制执行）
  - `action`（open/add/reduce/close + long/short）、`symbol`、`price`（可选）、`delta_size`、`leader_pos_before/after`、`notional_usd`、`leader_equity`、`leader_leverage`。
  - 价格缺失时按行情兜底，否则跳过并保留快照等待下一轮。 

## 主要后端接口（含路径）

- 交易员管理
  - `GET /api/my-traders`：当前用户交易员列表，返回信号源与复制配置。
  - `GET /api/traders/:id/config`：单个交易员详细配置。
  - `POST /api/traders`：创建交易员（支持信号源/复制配置）。
  - `PUT /api/traders/:id`：更新交易员（保留未传的信号源）。
  - `DELETE /api/traders/:id`：删除交易员。
  - `POST /api/traders/:id/start` / `POST /api/traders/:id/stop`：启动/停止。
  - `PUT /api/traders/:id/prompt`：更新自定义 Prompt。
  - `POST /api/traders/:id/sync-balance`：同步余额（可选）。
- 复制状态与日志
  - `GET /api/status?trader_id=...`：运行状态。
  - `GET /api/account?trader_id=...`：账户信息。
  - `GET /api/positions?trader_id=...`：持仓。
  - `GET /api/decisions?trader_id=...`：决策历史。
  - `GET /api/decisions/latest?trader_id=...&limit=n`：最近决策。
  - `GET /api/statistics?trader_id=...`、`GET /api/performance?trader_id=...`：统计与表现。
- 信号源配置
  - `GET /api/user/signal-sources` / `POST /api/user/signal-sources`：用户信号源 API 配置（币池等）。
- 系统与公共
  - `GET /api/config`：系统默认币种、杠杆等。
  - `GET /api/supported-models` / `GET /api/supported-exchanges`：支持的模型与交易所。
