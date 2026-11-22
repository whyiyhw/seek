# anchor-config-migrate

外部锚（eval v3 A1）第 7 题：**规则驱动的格式迁移**。v1 平铺 INI → v2 嵌套
JSON：section 嵌套、`retry.max` 改名 `max_attempts`、补 `server.timeout_s=30`
默认、数字保持数字、键序按文档规定。jq 断言路径/类型/值/键序全维度。

- **探什么**：从迁移文档读全规则并一条不漏地执行（漏一条默认值/错一个类型即红）——配置迁移是 agent 高频真实工种。
- **判分**：`check.sh` jq 断言（含 keys_unsorted 键序）。
- **难度预期**：中。规则五条、坑在键序与数字类型。
