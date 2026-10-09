# 计费示例（community.billing-examples）

计费插件为套餐规则提供自定义计量（`custom:community.billing-examples.<计量名>`）：

| 计量 | 含义 |
| --- | --- |
| `weighted_tokens` | 输出 token × 4 + 输入 token（输入含缓存读取与写入，与 `tokens.input` 一致） |
| `per_image` | 图片接口的输出图片张数 |

`computeUnits(usage, ctx)` 必须是纯函数：没有网络、存储与密钥，超时 5 ms；超时或抛错时该次计量按 0 记。
