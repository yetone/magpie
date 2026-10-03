# 实施方案：现有用量数据的分析看板（Issue #213）

## 1. 实施约束

以 [需求规范](spec.md) 为准，使用现有 `usage.jsonl`、Go 标准库与 GUI 组件。实现六张图、三个已有实体维度、服务端筛选与实体调用明细。

本次不扩充 `usage.Record`，不修改网关的计时或落盘。取消账号维度、尝试失败分析、真首字与前置等待分解、时间桶／状态切片／直方图下钻及其测试。

## 2. 改动文件

| 文件 | 内容 |
| :--- | :--- |
| `internal/usage/usage.go` | `Analyze`、三维度排行、`RecentCalls` |
| `internal/usage/usage_test.go` | 指标、筛选、有效样本、排序、下钻行为验证 |
| `internal/gui/usage.go` | `/api/analytics` 与 `/api/analytics/calls` |
| `internal/gui/assets/index.html` | 用量页入口、分析页、返回栏、时间与模式控制、筛选器、掩码按钮 |
| `internal/gui/assets/app.js` | 导航注册、整体与三套排行展示、服务端筛选、明细和详情 |
| `internal/gui/assets/app.css` | 三主题布局、条形图、数字卡片、CSS grid 抽屉 |
| `internal/gui/assets/i18n.js` | 指标、筛选、明细与说明的中英文文案 |
| `internal/gui/tests` | 按现有位置和命名惯例加入浏览器验证 |

## 3. 实施任务

### 任务 1：服务端聚合与实体明细查询

在 `internal/usage` 中使用已有 `Record` 定义 `Filter`（Model、Provider、Agent）、`AnalyticsData`、`RankingSuite`、`RankItem`、`TrendPoint`。

实现 `Analyze(p Period, filter Filter) AnalyticsData`：

1. 沿用用量页周期边界与桶粒度，只调用一次 `Load(since)`，读取周期记录。
2. 收集当前周期的模型、供应商、客户端筛选选项；应用复合筛选后一次遍历累计整体、三个实体维度和异常趋势。
3. 按 [指标约定](spec.md) 收集有效 TTFT 与解码样本，计算 TTFT P50/P95、解码速度、状态数、成本和缓存率。分位数使用过滤后的原始有效样本，速度使用总输出 / 总解码时间。
4. 各图按自己的门槛标记样本不足；缓存率统计输入量为 `input + cache_read`，缓存写入另计。全零缓存实体归入“无缓存记录 / 未知”。
5. 每个维度返回以下五组排行与相应结构化辅助数据：

   | 图表 ID | 字段 | 排序 |
   | :--- | :--- | :--- |
   | 1.1 | `by_error_rate` | 错误率 DESC |
   | 2.1 | `by_ttft` | TTFT P95 DESC |
   | 2.2 | `by_speed` | tok/s ASC |
   | 3.1 | `by_cost` | 成本 DESC |
   | 3.2 | `by_cache_rate` | 缓存率 ASC |

6. 响应包含 `summary`、`rankings.model`、`rankings.provider`、`rankings.agent`、`error_trend`、`filters`、`period`、`bucket`。无需传入展示模式。

实现 `RecentCalls(p Period, filter Filter, chartID string, limit int) []Record`：

- 在完整周期记录中应用 Model、Provider、Agent 条件及图表口径。
- 按 [实体下钻规范](dashboard.md) 排序后返回最多 50 条；默认限制为 50。
- `chartID` 为五种排行 ID；`1.2` 是只读趋势，不进入明细查询。

在 `internal/gui/usage.go` 注册两端点。分析参数为 `period/model/provider/agent`；明细另加 `chart_id/limit`。点击实体由前端映射到已有 Model、Provider 或 Agent 筛选，不引入账号、模式、时间桶或状态切片参数。

### 任务 2：页面与布局

在用量页头部添加 `#openAnalytics`，新增 `<main class="view" id="view-analytics" hidden>`，包含返回链接、周期、四种展示模式、动态筛选栏和三个主题区块。

页面结构与图表编号见 [仪表盘设计](dashboard.md)。宽屏双列，小屏单列。全部模式使用数字卡片，和主题磁贴重复的指标合并。明细与调用详情使用 CSS `grid-template-rows: 0fr -> 1fr`，180ms 过渡。

为全局 Hide emails 提供 `#analyticsMask` 按钮，注册 `pages` 时使用 `['#view-analytics', '#analyticsMask']`，处理现有会话等可能包含邮箱的文本。

### 任务 3：交互与双语

在 `show(v)` 的视图白名单及 URL 逻辑中注册 analytics。保存从用量页离开时的滚动位置、时间周期与标签，返回时恢复。

`loadAnalytics()` 请求服务端结果；全部模式绘制 `summary`，分组模式选择三套排行中的一套。切换按 X 模式清除 X 筛选、保留其他条件；仅当筛选条件发生改变才重新请求。周期或筛选变化总是重新聚合，前端不重算已汇总的交叉指标或分位数。

点击排行实体将其值合并到已有筛选参数，调用 `/api/analytics/calls`。全部模式卡片使用当前条件。明细在所点图表下方展开，同时仅有一处；点击记录展开已有字段详情。趋势没有点击与联动行为。

沿用 `app.js` 点击锚点与 ResizeObserver 补偿，禁止 `scrollIntoView`。补齐 `i18n.js` 中所有新增标签与辅助说明；渲染行为见 [交互设计](interaction.md)。

## 4. 验证方案

### 后端

- **缓存率与已有界面一致**：例如 `input=1000、cache_read=2000、cache_write=5000`，命中率为 `2000/3000`，写入量为 5000，完整提示词总量为 8000；分母 0 时没有有效命中率。
- **状态与有效样本**：成功、429、5xx、其他 4xx、499 混合；成功率按总调用数，错误率分子排除 499；非流式及失败记录不进入 TTFT 分位数；无输出或 `ms <= ttft_ms` 不进入速度累计。
- **排序方向**：同一维度的错误率、TTFT、成本由高到低，速度与缓存率由低到高；有效样本不足实体不参与主排序。
- **交叉筛选与分位数**：模型、供应商、客户端的交集与独立计算结果一致；同一响应中三个维度的排行对应同一筛选范围。
- **先筛后截断**：构造 100 条记录，其中某目标模型 20 条位于末段，返回全部 20 条目标记录；构造超过 50 条目标记录，返回图表口径排序后的前 50 条。

### 浏览器

使用项目现有 Chromium 与 WebKit 流程验证：

- 用量页到分析页继承周期，返回恢复滚动位置和标签。
- 模式选择对应排行；按 X 时清除 X 筛选；修改筛选触发服务器请求，新结果与明细条件一致。
- 端到端 TTFT、速度、成本和缓存条形均支持实体明细，趋势只读。
- 展开及收起明细、切换图表和展开单调用详情时，被点击元素的视口 Y 坐标变化不超过既有 2px 标准。
- 现有会话文本的邮箱随全局 Hide emails 切换；所有新增标签支持中英文。

## 5. 实施顺序

1. 服务端聚合与实体明细查询。
2. 页面骨架与六图布局。
3. 三套排行展示、服务端筛选、明细交互与 i18n。
4. 后端验证与 Chromium／WebKit 交互验证。
