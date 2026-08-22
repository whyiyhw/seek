# seek 配置 — API 密钥与 Provider 设置 / Configuration

seek 通过 `~/.seek/config.json`（或 `$SEEK_HOME/config.json`）管理 API 密钥、默认 provider 和其他全局设置。大多数用户不需要手动编辑这个文件——首次运行向导会自动配置。

> seek stores API keys, default provider selection, and global settings in `~/.seek/config.json`. Most users never hand-edit it — the first-run wizard handles setup.

---

## 1. 查找顺序（API 密钥） / Resolution order

API 密钥的查找优先级从高到低：

1. **环境变量**（最高优先级）——如 `DEEPSEEK_API_KEY`、`ANTHROPIC_API_KEY`、`OPENAI_API_KEY`、`GEMINI_API_KEY`。适合 CI、secret manager 或单次调用。
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
| `default_provider` | string | 默认使用的 provider（`deepseek` / `anthropic` / `openai` / `gemini`）。空字符串 = 不指定，走自动检测 |
| `providers` | object | 各 provider 的凭证配置，key 为 provider 名 |
| `providers.<name>.api_key` | string | API 密钥 |
| `path_prompt_done` | bool | Windows 上"添加到 PATH"提示是否已显示过 |
| `suggest_reply` | bool | 启用/禁用建议回复（suggested reply）功能。指针类型，缺省 = 启用 |
| `push_webhooks` | array | 推送通知 webhook 配置（见 [guide-webhooks.md](guide-webhooks.md)）|
| `session_notify_seconds` | int | 交互回合持续超过此秒数时触发推送通知。缺省 = 60s，设为 0 禁用 |

---

## 3. Provider 设置 / Provider setup

### 支持的 Provider

| Provider | 环境变量 | 配置名 |
|----------|---------|--------|
| **DeepSeek**（默认） | `DEEPSEEK_API_KEY` | `deepseek` |
| Anthropic | `ANTHROPIC_API_KEY` | `anthropic` |
| OpenAI | `OPENAI_API_KEY` | `openai` |
| Gemini | `GEMINI_API_KEY` | `gemini` |

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
