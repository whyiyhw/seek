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
| `providers.<name>.base_url` | string | 可选端点覆盖（**完整 POST URL**，不会自动补 `/chat/completions`）。当前用于 `glm`：Coding Plan 订阅必须指向 `/api/coding/paas/v4`，见 §3 |
| `path_prompt_done` | bool | Windows 上"添加到 PATH"提示是否已显示过 |
| `suggest_reply` | bool | 启用/禁用建议回复（suggested reply）功能。指针类型，缺省 = 启用 |
| `push_webhooks` | array | 推送通知 webhook 配置（见 [guide-webhooks.md](guide-webhooks.md)）|
| `session_notify_seconds` | int | 交互回合持续超过此秒数时触发推送通知。缺省 = 60s，设为 0 禁用 |
| `safety_check` | string | DeepSeek 模型安全校验（只认 `"high"` 一档；缺省/空 = 关）。命中拒答时回合内记 `[safety: 拒答（safety_check 命中）]`。**会话启动读一次**，不支持对话中切换（见下文「模型安全校验」）|
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

### GLM Coding Plan 订阅端点（`--provider=glm`）

智谱按 **URL 分账**，不按 key 形状分账：同一个 GLM key 打在 `/api/paas/v4` 扣的是**充值余额**（按 token 计费），打在 `/api/coding/paas/v4` 才走 **Coding Plan 订阅额度**。seek 的默认端点是 `https://open.bigmodel.cn/api/paas/v4/chat/completions`（多数用户是充值制，默认不动）；Coding Plan 订阅用户**必须显式覆盖**，否则会继续扣余额——余额为空时接口直接报 `1113`（「余额不足或无可用资源包」）。

端点覆盖优先级（高 → 低）：

1. `GLM_BASE_URL` 环境变量
2. `~/.seek/config.json` 的 `providers.glm.base_url`
3. `--base-url` 旗标
4. 默认 `https://open.bigmodel.cn/api/paas/v4/chat/completions`

```json
{
  "providers": {
    "glm": {
      "api_key": "<你的 GLM coding-plan key>",
      "base_url": "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions"
    }
  }
}
```

```bash
# 或单次运行覆盖（写在 .env / secret manager 里，别进命令行历史）
export GLM_BASE_URL=https://open.bigmodel.cn/api/coding/paas/v4/chat/completions
seek --provider glm
```

> 智谱官方 coding-plan quick-start 列出三个 Base URL：`/api/anthropic`（Anthropic 协议，seek 不走）、`/api/coding/paas/v4`、`/api/v1`。seek 的 OpenAI 兼容车道用 `/api/coding/paas/v4/chat/completions`——`base_url` 是完整 POST 目标，seek 不会替你补路径。
>
> 首启向导选 GLM 保存完 key 后会打印这段提示；不确定自己是哪种 key 时，先用默认端点跑一次，报 `1113` 就是 Coding Plan key，切到 `/api/coding/paas/v4` 即可。

**症状对照 / symptom → fix**

| 症状 | 根因 | 修法 |
|---|---|---|
| 充值余额一直在掉、Coding Plan 用量不动 | 仍在打默认 `/api/paas/v4` 端点（本节描述的坑） | 按上面任一方式覆盖 `base_url`（优先级：`GLM_BASE_URL` > config `base_url` > `--base-url`） |
| 报 `1113`「余额不足或无可用资源包」 | key 是订阅制，端点却仍是 `/api/paas/v4`，且充值余额已空 | 同上，切到 `/api/coding/paas/v4/chat/completions` |
| 报 `404`（响应体里 `path` 只剩 `/v4`） | `base_url` 不是完整 POST 目标——最常见是只写了 `…/api/coding/paas/v4`，漏掉 `/chat/completions`。seek 把该值**原样 POST**，不替你补路径 | 补全为 `…/api/coding/paas/v4/chat/completions` |
| 报 `401` | key 本身失效/已被轮换——与端点无关（端点写错回的是 404，不是 401） | 去控制台重新生成 key |

改配置文件后要**新开进程**：`base_url` 只在启动时读取，已在跑的 TUI / `seek acp` 会话不会热加载。判断自己有没有 Coding Plan 订阅：登录 [bigmodel.cn 控制台](https://open.bigmodel.cn/usercenter/apikeys)，订阅套餐页有用量记录即订阅制账户。

### OpenCode Go 订阅车道（`--provider=opencode`）

[OpenCode Go](https://opencode.ai/console/go) 是 $10/月的订阅制推理池（32 个开放权重 + 前沿模型：Kimi K3、Qwen3.8 Max、GLM-5.x、DeepSeek V4.x、Grok 4.7、GPT-6 Luna、Claude Haiku 5.5 等），**按 5 小时/周/月的请求数窗口限额，不按 token 扣费**——适合把便宜的批量任务（如 explore 子代理）放到这条车道。

- **Key 获取**：在 [console.opencode.ai](https://console.opencode.ai) 生成，`export OPENCODE_API_KEY=...`（或写 `providers.opencode.api_key`，首启向导也可直接选 OpenCode 粘贴）。
- **端点**：`https://opencode.ai/inference/go/openai/v1/chat/completions`（OpenAI 兼容；`--base-url` / `OPENCODE_BASE_URL` / `providers.opencode.base_url` 均可覆盖，优先级 env·config > flag，子代理路由同样遵守）。该路径取自 `GET https://opencode.ai/inference/go/v1/models` 里每个模型的 `api.chat-completions.base_url`；`/inference/openai/v1/...` 与旧的 `/inference/go/v1/...` 也都能路由，但 `/go` 前缀才把用量记在 Go 计划上。
- **默认模型**：`deepseek-v4.1-flash`（模型 id 以 [官方列表](https://opencode.ai/docs/console/models) 为准；接口下线/改名时用 `--model` 指定）。
- **可选模型**：Go 池里 20 个走 Chat-Completions 原生的模型都进了菜单（TUI `/model` 选择器 + ACP `session/new` 下拉），名单的唯一来源是 `pricing.OpenCodeGoModels`：deepseek-v4.1-flash / v4-pro、glm-5.3 / 5.2 / 5.3-flash、kimi-k3 / k3.1-pro / k2.7-code、minimax-m3.1-flash、mimo-v2.6-flash / pro、longcat-2.0、hy4 / hy4-preview / hy3、step-5-preview，外加 4 个限时免费模型 beam-free / hy4-flash-free / longcat-2.5-free / iceman-contributor-free。**没进来的**：grok-4.7 / gpt-5.6 / gpt-6 / muse-spark（只走 Responses API）、claude-haiku-5-5 / minimax-m3 / qwen 系列（只走 Anthropic messages）、gemini-4-argon（generate-content）——seek 的兼容适配器说不了这些协议。
- **动态目录**：上面的表是编译期基线，不是天花板。车道启动时（`--provider=opencode` 且有 key）会带 key 请求 `GET /v1/models`：先装 `~/.seek/opencode-catalog.json` 里的缓存（24h 内有效；最多容忍 7 天陈旧），再后台刷新一次。刷新成功后菜单、成本估算、`/compact` 阈值都跟着换新——厂商新上的模型不用等发版；被下线的模型也会自动从菜单消失。抓取失败、超时、403、坏 JSON、缓存损坏一律**静默退回编译期基线**，不阻塞启动、不影响推理。`SEEK_OPENCODE_CATALOG=off` 可整体关闭（完全离线）；TUI 里 `/model refresh` 可手动刷新一次。目录只喂菜单/成本/上下文，**不进 system prompt**，因此不影响前缀缓存。
- **客户端识别**：OpenCode 通过请求头识别第三方客户端——seek 会自动发送 `User-Agent: seek/<version>` 和每会话稳定的 `x-opencode-session: <会话id>`（主会话与子代理各持各的 id）。缺 `x-opencode-session` 会被网关直接 `400 MissingSessionID` 拒掉；UA 是通用 SDK 名时按文档可能退化成按 token 扣账户余额。
- **免费模型**：官方定义是「`opencode` / `opencode-go` 池里 input price 为 0 的模型」——同一个网关、同一把 key，只是不计量（`go.monthly_usage: null`），seek 的报价卡也是 $0，选它跑出来的成本显示就是 `$0`。它们都有各自的限速（如 longcat-2.5-free 100 请求/天）与地区/训练条款；工作区若设了 `OPENCODE_DISABLE_FREE_MODELS=1`，选中会在请求时收到 `Model unavailable` 这类带内错误——不是静默回退。
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

### 模型安全校验（`safety_check`）

```json
{
  "safety_check": "high"
}
```

- **默认关**。开启后 DeepSeek 对模型的输入输出过一遍安全审核，命中风险时拒答（`finish_reason: "refusal"`）。seek 把拒答记为 assistant 消息的 in-band 标记 `[safety: 拒答（safety_check 命中）]`，**回合正常结束**——不报错、不重试、不中断会话。
- 只认 `"high"` 一档（官方目前只提供这一档）；缺省 / 空 / 其它值一律视为关。
- **会话启动时读取一次**，不支持对话中切换：该值随每个请求体发送，中途改动会改变请求字节、击穿前缀缓存（与 `/lang` 同纪律）。
- 仅对 DeepSeek 车道生效；其它 provider 忽略此键。
- 自用默认关的理由：拒答会削弱 agent 在安全研究、漏洞分析等合法任务上的能力，并增加延迟；seek 自身没有对外服务义务。
- **合规注记**：若你**对外提供服务**（把 seek 接进面向他人的产品或服务），开启本参数、并承担内容安全审核、大模型登记、AI 生成内容标识等义务，属于**你（服务运营者）的法定义务侧事项**——seek 只提供这个开关，不承担审核或合规责任。

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
seek --help              # 子命令清单 + 所有 CLI 标志
seek <command> help      # 单个子命令的详细帮助（如 seek serve help）
seek --provider <name>   # 临时指定 provider

# 常驻控制平面 + 内置浏览器工作台
seek serve --frontend=web      # 监听 127.0.0.1:8790，启动时把带 token 的 URL 打到 stderr
seek serve --frontend=web -h   # -h/--help 放在标志之后同样有效

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
