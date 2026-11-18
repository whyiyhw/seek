# seek 配置 — API 密钥与 Provider 设置 / Configuration

seek 通过 `~/.seek/config.json`（或 `$SEEK_HOME/config.json`）管理 API 密钥、默认 provider 和其他全局设置。大多数用户不需要手动编辑这个文件——首次运行向导会自动配置。

> seek stores API keys, default provider selection, and global settings in `~/.seek/config.json`. Most users never hand-edit it — the first-run wizard handles setup.

---

## 1. 查找顺序（API 密钥） / Resolution order

API 密钥的查找优先级从高到低：

1. **环境变量**（最高优先级）——如 `DEEPSEEK_API_KEY`、`ANTHROPIC_API_KEY`、`OPENAI_API_KEY`、`GEMINI_API_KEY`、`GLM_API_KEY`。适合 CI、secret manager 或单次调用。
2. **`~/.seek/config.json`**——由首次运行向导写入或用户手动编辑。
3. **空**——无密钥配置时，seek 会自动启动配置向导。

> 环境变量始终优先于配置文件，这样 CI 等场景下不会意外读到过期的本地密钥。

---

## 2. 配置文件格式 / Config file format

```json
{
  "default_provider": "deepseek",
  "providers": {
    "deepseek": {
      "api_key": "sk-..."
    },
    "anthropic": {
      "api_key": "sk-ant-..."
    }
  }
}
```

### 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `default_provider` | string | 默认使用的 provider（`deepseek` / `anthropic` / `openai` / `gemini` / `glm` / `opencode`）。空字符串 = 不指定，走自动检测 |
| `providers` | object | 各 provider 的凭证配置，key 为 provider 名 |
| `providers.<name>.api_key` | string | API 密钥 |
| `path_prompt_done` | bool | Windows 上"添加到 PATH"提示是否已显示过 |
| `suggest_reply` | bool | 启用/禁用建议回复（suggested reply）功能。指针类型，缺省 = 启用 |
| `push_webhooks` | array | 推送通知 webhook 配置（见 [guide-webhooks.md](guide-webhooks.md)）|
| `session_notify_seconds` | int | 交互回合持续超过此秒数时触发推送通知。缺省 = 60s，设为 0 禁用 |
| `agents` | object | 子代理类型的默认路由覆写（见下节；键 = 类型名，值 = 仅路由旋钮）|

---

## 3. Provider 设置 / Provider setup

### 支持的 Provider

| Provider | 环境变量 | 配置名 |
|----------|---------|--------|
| **DeepSeek**（默认） | `DEEPSEEK_API_KEY` | `deepseek` |
| Anthropic | `ANTHROPIC_API_KEY` | `anthropic` |
| OpenAI | `OPENAI_API_KEY` | `openai` |
| Gemini | `GEMINI_API_KEY` | `gemini` |
| GLM（智谱 bigmodel.cn） | `GLM_API_KEY` | `glm` |
| OpenCode（Go 订阅） | `OPENCODE_API_KEY` | `opencode` |

GLM 与 OpenCode 无需手编 config.json——首启向导（§4）可直接选中并粘贴 key。

### OpenCode Go 订阅车道（`--provider=opencode`）

[OpenCode Go](https://opencode.ai/console/go) 是 $10/月的订阅制推理池（32 个开放权重 + 前沿模型：Kimi K3、Qwen3.8 Max、GLM-5.x、DeepSeek V4.x、Grok 4.7、GPT-6 Luna、Claude Haiku 5.5 等），**按 5 小时/周/月的请求数窗口限额，不按 token 扣费**——适合把便宜的批量任务（如 explore 子代理）放到这条车道。

- **Key 获取**：在 [console.opencode.ai](https://console.opencode.ai) 生成，`export OPENCODE_API_KEY=...`（或写 `providers.opencode.api_key`，首启向导也可直接选 OpenCode 粘贴）。
- **端点**：`https://opencode.ai/inference/go/v1/chat/completions`（OpenAI 兼容；`--base-url` 可覆盖）。
- **默认模型**：`deepseek-v4.1-flash`（模型 id 以 [官方列表](https://opencode.ai/docs/console/models) 为准；接口下线/改名时用 `--model` 指定）。
- **客户端识别**：OpenCode 通过请求头识别第三方客户端——seek 会自动发送 `User-Agent: seek/<version>` 和每会话稳定的 `x-opencode-session: <会话id>`（主会话与子代理各持各的 id）。缺少这两个头的请求可能退化成按 token 扣账户余额。
- **免费模型限制**：`*-Free` 结尾的模型只能在 OpenCode 自家客户端内使用，seek 不支持。
- **隐私**：Go 计划默认屏蔽「用请求训练模型」的付费端点（详见官方文档）。

注意：状态栏里该车道显示的美元成本是按官方 per-token 牌价的**估算展示**（订阅本身不按 token 计费）。

### 切换默认 Provider

```bash
# 通过环境变量临时切换
DEEPSEEK_API_KEY=sk-xxx seek

# 通过 --provider 标志
seek --provider anthropic

# 在配置文件中设置默认
# 编辑 ~/.seek/config.json 设置 default_provider 字段
```

---

## 4. 首次运行向导 / First-run wizard

首次运行 seek 且未配置 API 密钥时，会自动启动交互式向导：

1. 检测已设置的环境变量
2. 提示选择 provider
3. 输入 API 密钥（或确认使用已检测到的环境变量）
4. 写入 `~/.seek/config.json`

---

## 5. 其他配置 / Other settings

### 建议回复（Suggest Reply）

控制是否在对话中显示 AI 预测的下一条回复：

```json
{
  "suggest_reply": false
}
```

缺省为 `true`。设为 `false` 完全禁用。

### 推送通知

将 cron 任务完成、autopilot 结果、长时间交互回合的结束通知推送到手机。配置方式见 [guide-webhooks.md](guide-webhooks.md)。

### 子代理类型路由（`agents` 段）

给子代理类型设**默认路由**，免得每次 `agent` 工具调用都带 provider/model 参数（feature-orchestration.md D6/D14）：

```json
{
  "agents": {
    "explore": { "provider": "glm", "model": "glm-5.3" },
    "plan":    { "effort": "low", "max_turns": 40 }
  }
}
```

- 键 = 类型名（内置 `general-purpose` / `explore` / `plan`，或文件定义的自定义类型）；值只允许路由旋钮 `provider` / `model` / `effort` / `max_turns`，空 = 继承。
- **config 只换脑子不换权限**：工具集、prompt、workflow 不能在这里改——那些属于 `agents/*.md` 定义文件（见 [guide-subagent.md](guide-subagent.md)）。
- 优先级：内置默认 < config 段 < `agents/*.md`（同名时文件层直接压过 config）。未知键名启动时警告并忽略（前向兼容）。
- `seek agents list` 可查看合并后的注册表（project > user > config > builtin）。



## 6. CLI 参考 / CLI reference

```bash
# 查看当前配置
seek --help              # 查看所有 CLI 标志
seek --provider <name>   # 临时指定 provider

# 环境变量（最高优先级）
export DEEPSEEK_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."
export OPENAI_API_KEY="sk-..."
export GEMINI_API_KEY="..."
```

### 持久化路径

| 平台 | 配置文件路径 |
|------|-------------|
| Linux / macOS | `~/.seek/config.json` |
| Windows | `%USERPROFILE%\.seek\config.json` |

> 可通过 `$SEEK_HOME` 环境变量覆盖 `~/.seek` 的位置。
