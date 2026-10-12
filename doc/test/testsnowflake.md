# testsnowflake — 测试用雪花节点

代码：`test/testsnowflake/snowflake.go`。

`InitTestSnoflake(id)` 以指定 worker ID 初始化全局雪花节点（内部调用 `utils.InitSnowflake`，失败直接 panic）。

- 单测开始时调用一次（如 `testsnowflake.InitTestSnoflake(3)`），worker ID 按测试包错开避免冲突。
- 依赖 `BaseModel.BeforeCreate` 自动填充雪花 ID 的用例必须先初始化。
