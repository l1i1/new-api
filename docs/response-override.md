# Response Override（响应体改写，逐渠道可编辑）

## 一句话

渠道的**响应体**在返回给客户端之前，按该渠道自己的可编辑文档改写——语法与既有的 `param_override`（请求体改写）完全一致，改配置即可生效，不需要编译或发版。

## 为什么需要它

有些上游做不到官方形态（例如官方在某形态下不产生 reasoning，上游照产生）。这时只有两条路：

1. 把这个请求**路由到别的渠道**（现在 fitpolicy 的做法）；
2. **网关就地改写响应**，让这个渠道也能按优先级承接。

Route 的语义（2026-10-03 定稿）是"能做到拟合官方的渠道按优先级来"。请求体改写（`param_override`）已经覆盖了"改请求就能拟合"的情况；`response_override` 补上"只能改响应才能拟合"的情况。

## 文档形状

存在渠道字段 `response_override`（TEXT，JSON），与 `param_override` 同语法：

```json
{
  "operations": [
    {
      "path": "choices.0.message.reasoning_content",
      "mode": "delete"
    },
    {
      "path": "choices.0.finish_reason",
      "mode": "set",
      "value": "stop",
      "conditions": [
        {"path": "choices.0.message.reasoning_content", "mode": "full", "value": "secret"}
      ]
    }
  ]
}
```

- `mode` 复用请求侧全部操作词汇（`set`/`delete`/`move`/`copy`/`replace`/`regex_replace`/`trim_*`/`prune_objects`…）。
- `conditions` 支持 AND/OR、比较模式（`full`/`prefix`/`suffix`/`contains`/`gt`/`gte`/`lt`/`lte`）、取反与缺键行为；条件路径读的是**当前这段响应 JSON**。
- 管理台渠道抽屉里有独立字段 + 可视化编辑器（与请求侧共用同一个对话框）。

## 强制约束：只做形态，不碰计量

**写时校验**拒绝任何指向 `usage*` 路径（含 `from`/`to`）的操作，以及响应头类操作（响应头此时已在线上）。**运行时**再拦一道：绕过 API 直接写库的文档里，这类操作也会被跳过并记日志。

原因：上游真实生成、并已按上游计量计费的 token，如果被网关改写数字，客户端看到的账目就不是它被计费的口径——这正是 2026-10-02 客户投诉的那种形态（"看不到的思考却计了费"）。形态可以拟合，账目必须诚实。

## 生效与可观测

- 逐渠道保存 → 渠道缓存 → 配置纪元广播，**全舰队约 2 秒**生效，无需发版（与 `param_override` 同路径）。
- 每次改写在请求日志的 `other` 里留下 `ro` 审计数组（请求侧对应的是 `po`），例如 `"ro":["delete choices.0.message.reasoning_content"]`；这样"客户端响应与上游不同"永远可归因。
- 作用范围：OpenAI 兼容路径的**非流式整体响应**与**流式逐事件**（流式在写出前逐 chunk 改写，条件按单事件求值）。
- 不适用/不写规则时零开销（无文档直接透传）。

## 与路由的闭环

配置 `response_override` → 探测套件走的就是这条改写后的链路 → 实测通过 → 该渠道获得该形状的资格 → 按优先级承接。反之，改写没能真正拟合（探测仍发散）就继续顺延到下一优先级渠道——判据始终是实测，不是配置声明。
