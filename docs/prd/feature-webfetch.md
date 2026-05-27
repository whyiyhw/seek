# WebFetch 工具：plan-analyze 外部文档访问的安全口子

**主题**：给 agent 一条**狭窄、有界、读-only** 的 HTTP GET 路径，让 plan-analyze 模式（以及任何模式）能在不开 bash 的前提下访问外部文档。本质上是 `read` 的远程版本：路径换成 URL，inside-CWD 验证换成 SSRF 防御。

**状态**：🚀 v1 已上线。W1-W6 全部实施完成；详见 §六 阶段交付（每阶段标 ✅ + 实际落地处）。

**触发起因**：2026-05 一次 plan 模式会话中，模型尝试 `curl -sL https://docs.anthropic.com/...` 被 Phase D 的 bash 只读白名单 deny（详见 [`docs/prd/feature-plan-mode.md`](feature-plan-mode.md) §八）。`curl` 不该进白名单（flag 组合太多、`file://` / localhost / 上传文件等都能搞事），但模型在 plan-analyze 里**完全没有合法的外部读取路径**也是事实。本 PRD 是这条缺口的正解。

---

## 一、目标与范围

### 1.1 解决的问题

在 plan-analyze（`ModePlan`）以及任意模式下：

- 模型经常需要参考外部权威文档：API spec、第三方库文档、IETF / RFC、GitHub raw README、官方教程
- 现状只有三条路：(a) 退出 plan 模式失去 confirmation gate；(b) 让用户 paste 进 chat（多余动作）；(c) 模型脑补（容易胡）
- 给一个**专用、有边界、模型可主动调用**的 HTTP GET 工具，把这一类需求收口

### 1.2 设计哲学

**不是通用 HTTP 客户端，是"远程 read"。** 跟现有 `read` 工具的姿态完全对齐：

| | `read` 工具 | `webfetch` 工具 |
|---|---|---|
| 输入 | 本地路径 | HTTPS URL |
| 安全边界 | path-inside-CWD | URL 不指向私网 / loopback / file:// |
| 操作 | 读字节 | HTTP GET |
| 写? | 否 | 否（永远） |
| 输出 cap | 50 行 | 64 KiB（默认）/ 256 KiB（max） |
| 在 plan-analyze? | 允许 | 允许 |

模型用 `webfetch` 应该像用 `read` 一样自然：拿走内容，不会有副作用。

### 1.3 范围之外

- ❌ **POST / PUT / DELETE / 任何写操作** —— 永远不做
- ❌ **任意 header / cookie / Authorization 注入** —— 模型不能控制请求 header（防 exfil + 防绕过认证）
- ❌ **响应内容缓存 / 历史**—— 每次 fetch 独立；fetched 内容就在 transcript 里
- ❌ **多 URL 并发抓取 / 爬虫式扩散**—— 一次一个 URL
- ❌ **PDF / image / binary parsing**—— 仅 text/json/xml/markdown 系
- ❌ **JS 渲染 / headless browser**—— 静态 HTTP，没有 DOM
- ❌ **robots.txt 检查**—— 不是爬虫；模型只拉用户授意的 URL
- ❌ **认证 / OAuth / session 持有**—— 想要带 token 访问的，去用 MCP server

如果未来要支持上述任何一条：**新工具**，不是扩 webfetch。

---

## 二、设计

### 2.1 工具骨架

```
internal/tools/webfetch/
├── webfetch.go      # Tool 实现 + Execute
├── validate.go      # URL 验证（scheme / host / 私网 IP / 端口）
├── render.go        # HTML → text 简化
├── *_test.go
```

包级 `New() Tool`（无依赖注入 —— 工具自身的验证 IS the gate），注册在 `cmd/seek/main.go` 跟其他 read-only 工具同一块。

### 2.2 Schema

```json
{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "Absolute https:// URL. http:// is rejected unless SEEK_WEBFETCH_ALLOW_HTTP=1 is set in the env. file://, ftp://, gopher://, etc. are always rejected. URLs resolving to private/loopback/link-local IPs are rejected after DNS resolution (SSRF defense). Redirects are followed (max 5) but each hop is re-validated."
    },
    "max_bytes": {
      "type": "integer",
      "minimum": 1024,
      "maximum": 262144,
      "description": "Maximum response body size in bytes. Default 65536 (64 KiB). Body is truncated past the cap and the tool result notes truncation."
    }
  },
  "required": ["url"],
  "additionalProperties": false
}
```

**Schema bytes 是 package-level `[]byte` 常量**（per CLAUDE.md `Code conventions`），用于 prefix cache 稳定。

### 2.3 URL 验证（SSRF 防御）— 多层

**Layer 1：URL parse + scheme check**
- 必须能 `url.Parse` 成功
- Scheme 必须是 `https`（或 `http` 当 `SEEK_WEBFETCH_ALLOW_HTTP=1`）
- 必须有 Host 字段（reject `https:///foo`）

**Layer 2：Hostname-level reject list**
- 字面 `localhost` / `*.localhost`（多大小写）
- `0.0.0.0`、`::` 等"绑定到任意"占位
- 空 host

**Layer 3：Port 限制**
- 默认只放 80 / 443（标准）
- 显式 port 也得在 80/443/8080/8443 白名单里（防止打内网 5432 / 3306 / 6379 / 22 / 11211 等）

**Layer 4：DNS 解析后的 IP 验证 —— 这是 SSRF 的关键层**

Hostname-level reject 不够：`evil.com` 可以 DNS-resolve 到 `127.0.0.1`。所以：

1. 在发请求**前**，先 `net.LookupIP(host)` 拿到所有解析结果
2. 对每个 IP 检查是否属于：
   - Loopback：`127.0.0.0/8`、`::1/128`
   - Link-local：`169.254.0.0/16`、`fe80::/10`
   - RFC1918 私网：`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`
   - ULA：`fc00::/7`
   - Multicast / broadcast / unspecified：`0.0.0.0`、`255.255.255.255`、`224.0.0.0/4`
   - CGNAT：`100.64.0.0/10`
3. 任一 IP 命中 → reject
4. 自定义 `http.Transport.DialContext` 把解析结果传进去，避免 TOCTOU（不要让 Go 重新解析、不要让 server 通过 DNS rebinding 攻击）

**Layer 5：Redirect re-validation**
- `http.Client.CheckRedirect` 钩子在每次 30x 时对 `req.URL` 跑 Layer 1-4
- 重定向到私网 → 立即终止
- Max 5 hops（防循环 + 防滥用）

### 2.4 请求形态（极其简朴）

- **Method**: GET（永远）
- **Headers**:
  - `User-Agent: seek-webfetch/<seek-version>` （固定，模型不可控）
  - `Accept: text/html,application/xhtml+xml,application/xml;q=0.9,application/json,text/plain,text/markdown,*/*;q=0.5`
  - `Accept-Encoding`: gzip / br（节省带宽 + 透明解码）
- **没有 cookies、没有 Authorization、没有自定义 header**
- **Timeout**: 30s（context-driven，可被 agent ctx 取消）
- **Proxy**: 尊重标准 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` 环境变量（Go stdlib 默认行为）

### 2.5 响应处理

**Content-Type 白名单**：
- `text/html`、`text/plain`、`text/markdown`、`text/xml`
- `application/json`、`application/xml`、`application/rss+xml`、`application/atom+xml`、`application/ld+json`
- `application/javascript`、`application/x-www-form-urlencoded`（罕见但合法的文本类）

**Reject**：
- `image/*`、`video/*`、`audio/*`
- `application/octet-stream`、`application/pdf`、`application/zip`、所有 binary
- 空 / 缺失的 Content-Type → reject（防偷渡）

**HTTP 状态码**：
- 2xx → 正常返回 body
- 3xx → Go 自动 follow（受 CheckRedirect 约束）
- 4xx / 5xx → reject + 状态码 + 简短 body 摘要给模型

**HTML → text 简化**（仅 `text/html`）：
- 用 `golang.org/x/net/html` tokenize
- 丢弃 `<script>`、`<style>`、`<noscript>` 块的内容
- 保留正文文本，简单空白规范化
- **不**做 markdown 重建（保留段落感即可；模型自己能读半结构化文本）
- 其他 text/* 和 application/json 直接 raw 返回

**Size cap**：
- 默认 64 KiB，max 256 KiB（参数 `max_bytes`）
- 读到 cap 立即停（`io.LimitReader`）
- 截断时在返回 body 末尾追加 `... (truncated at N bytes)`

### 2.6 返回给模型的格式

```
URL: https://docs.anthropic.com/en/docs/claude-code/overview
Final URL: https://docs.anthropic.com/en/docs/claude-code/overview/  (after 1 redirect)
Status: 200 OK
Content-Type: text/html; charset=utf-8
Size: 12480 bytes

<rendered text content...>
```

如果 truncated：
```
... (truncated at 65536 bytes; ~22000 bytes more were available — re-fetch with max_bytes=131072 if needed)
```

如果 fetch 失败（DNS / network / 4xx / 5xx）：返回**错误形式的 tool result**（per CLAUDE.md permission denials are tool results 的精神），让模型 reasonng 而非崩。

### 2.7 权限模型

**不引入新的 `permission.Kind`**。webfetch 跟 `git` 工具一样是 **read-only by construction** —— 它内部的 URL allowlist + GET-only + no-write 就是安全边界，不需要 `permission.Policy.Check`。

| 模式 | webfetch 行为 |
|------|--------------|
| `ModeYolo` | 允许 |
| `ModeAsk` | 允许，不弹 y/N（一致于 `git` 工具） |
| `ModePlan`（plan-analyze） | 允许 |
| `ModeDeny` | 允许（防御性地，这是 read-only 操作） |

**额外 opt-out**：环境变量 `SEEK_NO_WEBFETCH=1`（或值 `true`/`yes`/`on`）在启动时禁用。tool registry 完全不注册 webfetch；模型看不到这个工具就不会调。用例：

- 隔离 / air-gapped session
- 隐私敏感环境（不想任何 outbound network 调用）
- CI / 测试运行（避免 flakiness）

`SEEK_WEBFETCH_ALLOW_HTTP=1` 单独控制是否放 `http://`（默认禁，企业内网 docs 启用）。

### 2.8 错误分类与文案

每类错误返回一段对模型友好的 tool result string：

| 类别 | 触发 | 给模型的文案前缀 |
|------|------|--------------|
| `[webfetch: invalid url]` | URL parse 失败 / scheme 非 http(s) | "URL must be absolute https:// (or http:// with SEEK_WEBFETCH_ALLOW_HTTP=1). Got: \<url\>" |
| `[webfetch: blocked target]` | 命中 SSRF blacklist | "URL host resolves to a private/loopback address (\<ip\>) — webfetch only allows public targets. Try a public mirror or the official docs URL." |
| `[webfetch: blocked port]` | 非标准端口 | "Port \<n\> is not in the allowlist (80, 443, 8080, 8443). Most public services use 443." |
| `[webfetch: redirect loop]` | 超过 5 hops | "Too many redirects (>5). Final URL: \<u\>." |
| `[webfetch: redirect to private]` | 30x 跳到内网 | "Redirect chain ended at private address — refused. Final attempted URL: \<u\>." |
| `[webfetch: timeout]` | 30s 超时 | "Server did not respond within 30s." |
| `[webfetch: bad content-type]` | Binary / 缺失 CT | "Response Content-Type \<x\> is not text-like. webfetch only handles text/json/xml/markdown." |
| `[webfetch: http error]` | 4xx / 5xx | "HTTP \<code\> \<text\>. Body snippet: \<first 200 chars\>." |
| `[webfetch: too large]` | Body 大于 256 KiB 且 max_bytes 已撑满 | （body 截断 + 末尾追加 truncated note —— 这不算 fatal） |

每条都包含可执行建议，让模型在 LLM 内闭环修正（per "denials are tool results, not fatal errors"）。

---

## 三、关键决策

### 3.1 为什么不复用 / 扩展 bash 工具的白名单加 `curl`

详见 `feature-plan-mode.md` §八触发起因部分 + 上述 §1.2。简要：

- `curl` 的 flag 矩阵太大，任何白名单 regex 都会有缝（`-o` 写盘、`-d` POST、`-X PATCH`、`file://`、`--upload-file` 上传）
- 白名单的设计哲学（`internal/tools/bash/readonly.go` 头注释）就是"保守优先" —— 给 curl 开口子会撕开第一个洞
- 专用工具可以**强制** GET-only、强制 URL 验证、强制 size cap，这些是 bash 永远做不到的

### 3.2 为什么 SSRF 防御要做 DNS-after-validation

Hostname-level reject 远远不够。攻击向量：

- DNS rebinding：`evil.com` TTL=0，第一次解析公网、再解析私网
- 公共 DNS 重定向：`xip.io`、`nip.io` 类服务可以把任意 IP 编码进 hostname
- IPv6 隧道地址：`::ffff:127.0.0.1` 看起来像 IPv6 但实际是 loopback

正确做法：**在 Dialer 层把 IP 验证一次，并直接把验证过的 IP 传给底层 connection**。Go `net.Dialer.Control` + 自定义 `DialContext` 是标准模式，参考 `https://github.com/google/go-safeweb/...` 的安全 client 实现。

### 3.3 为什么不引入 `permission.Kind`，靠工具自身边界

跟 `git` 工具同款决策（详见 `permission.go:KindGit` 注释）：

- 引入新 Kind 会迫使所有 mode 路径处理新分支（4 个 mode × 处理逻辑）
- webfetch 的"危险性"100% 体现在 URL 合法性上 —— 这是工具内部知识，不是 policy 该决策的事
- 在 ModeAsk 弹 y/N 会让每次拉文档都变成 prompt 疲劳源，违反 `git` 工具同款 UX 期望

**SEEK_NO_WEBFETCH** 环境变量是粗粒度 kill switch，足够覆盖"我这次不想让 agent 上网"的场景，无需引入运行时 mode。

### 3.4 为什么 HTML → text 而不是返回原 HTML

- 200 KiB 的 HTML 可能只有 20 KiB 的有效文本，余下是 script / style / nav / 模板
- 模型再聪明也得花 token 读 boilerplate
- HTML→text 在工具层做一次，省 transcript 字节、省 cache 字节
- 简化要克制：不做 markdown 重建（goldmark / x/net/html 都能做但增加复杂度）；保留段落、丢 script/style 即可

### 3.5 为什么 default max_bytes = 64 KiB

样本观察：

- Anthropic docs 一页 typical 8–30 KiB（HTML→text 后）
- pkg.go.dev 一个 type 页 5–15 KiB
- IETF RFC 文档 50–150 KiB
- GitHub raw README 5–80 KiB

64 KiB 覆盖 80%+ 场景；模型遇到 truncation 时知道用 `max_bytes=131072` 重抓。256 KiB 上限是硬安全阈值（cache 单 turn budget + 防滥用）。

---

## 四、非目标

- ❌ **多 URL 批量抓**：一次一个；模型要爬目录就 prompt 用户用 MCP / 外部工具
- ❌ **任意 HTTP method**：永远 GET
- ❌ **自定义 headers**：永远固定 set
- ❌ **JavaScript 渲染** / SPA：模型遇到 SPA 应该改去 README / GitHub / docs 站
- ❌ **PDF 解析**：单独工具的事（可以 future `pdfread`）
- ❌ **图片 OCR**：单独工具的事
- ❌ **WebSocket / Server-Sent Events / 长流**：明确反向（HTTP request-response only）
- ❌ **缓存 / ETags**：每次独立 fetch；缓存策略是另一个项目
- ❌ **取代 MCP `fetch` 服务器**：MCP 是 opt-in 强能力（可带 auth、cookies、复杂业务）；webfetch 是 built-in 弱能力（绝对只读、绝对公网）。两者并存。
- ❌ **HTML 链接展开 / 转 markdown**：v1 朴素文本即可

---

## 五、风险与 mitigation

| 风险 | 概率 | 影响 | mitigation |
|------|------|------|--------|
| SSRF 通过 DNS rebinding 击穿 | 中 | 内网信息泄露 | DNS-after-validation + 自定义 DialContext 把验证过的 IP 传下去，禁止运行时重新解析 |
| 模型把敏感 path / 文件名拼进 URL 当 query 发出去 | 低 | exfil 用户 repo 内容到第三方 | URL 验证仅看 host/port/scheme；模型对 URL 内容负责。可考虑 query string 长度 cap（≤ 2 KiB），但 v1 不做 |
| 大 body 导致 transcript / cache 膨胀 | 中 | token 浪费 | max_bytes hard cap 256 KiB + truncation note 引导模型再抓 |
| Content-Type 误判（server 撒谎说 text/html 实际 binary） | 低 | 模型看到乱码 | 已经 LimitReader 截断；模型读到乱码会自然 ask_user / 换 URL |
| Proxy 配置泄露（HTTPS_PROXY 指向用户公司代理） | 低 | 公司代理日志可见 fetched URL | Go stdlib 默认行为；用户自己设的代理是自己的事，文档里说明 |
| 模型滥用为爬虫（一轮抓 50 个 URL） | 中 | 远端服务被打爆 + 用户被 IP 封 | 单次 Execute 单 URL；agent loop 自然限流；max_turns 是上限 |
| HTTPS 证书过期 / self-signed | 低 | 合法 fetch 失败 | 不放 `--insecure` 等价物；模型遇到再让用户用 MCP |
| 服务端 redirect 到 `file://` | 极低 | 本地文件读取 | CheckRedirect 重新校验 scheme |

---

## 六、阶段交付（✅ 已完成）

| 阶段 | 内容 | 落地处 |
|------|------|-------|
| W1 ✅ | URL/IP validator + 5 层 SSRF 防御 + 表驱动测试 | `validate.go` + `validate_test.go`（~280 行 + ~300 行测试，覆盖 scheme / host / port / 私网 IP / DNS rebinding 模式） |
| W2 ✅ | Tool 实现：自定义 `http.Client` (DialContext + CheckRedirect + Transport)、size cap、Content-Type 过滤、错误分类 | `webfetch.go` (~380 行) + `webfetch_test.go` (~330 行 httptest 覆盖 happy/30x/4xx/5xx/timeout/binary CT/truncation/SSRF) |
| W3 ✅ | HTML→text 简化：`golang.org/x/net/html` tokenize + script/style/noscript 剥离 + 段落保留 + 空白规范化 | `render.go` (~160 行) + `render_test.go` (~150 行测试) |
| W4 ✅ | `cmd/seek/main.go` 注册 + `SEEK_NO_WEBFETCH` / `SEEK_WEBFETCH_ALLOW_HTTP` 环境变量；通用 `envBoolTrue` helper | `cmd/seek/main.go` (~25 行新增 + `envBoolTrue` 提取) + `envgate_test.go` |
| W5 ✅ | 端到端验证集成在 W2 httptest 套件中（redirect-to-blocked / 拒 file:// / 拒 localhost / 拒 bad port / 大文件截断 / ctx 取消） | 见 `webfetch_test.go` |
| W6 ✅ | Skill 文档更新 | `internal/skill/builtin/plan-mode.md` mode reminder 段补 webfetch + curl 反例 |

**实际工作量**：约 1 天（比 PRD 估计的 1.5 天略快，因为 W5 集成进了 W2 httptest 套件，没单开 e2e）。`go vet` clean、`go test -race ./...` 45 包全绿、0 failure。

**关键实施补充 vs PRD**：
- `Tool.skipIPValidation` 字段：package-private 字段，**仅供单测**使用让 httptest server (127.0.0.1) 能 dial。生产代码绝不可设。
- `ssrfDialContext` 实现："resolve all IPs → validate each → dial validated IP 字面量"，防 DNS rebinding 攻击。
- `formatErrorResult` / `formatHTTPError` / `formatSuccess` 三个文案 helper：所有错误都走 `[webfetch: <category>]` 前缀（per PRD §2.8），让模型在 tool result 里识别错误类别后自决断。

### 6.1 测试覆盖（按 CLAUDE.md 失败路径强制）

- **URL validation 表驱动**：每一类 reject（私网 IP / link-local / loopback / 非标准 port / 非 https / file:// / 空 host）至少一条
- **DNS rebinding 模拟**：mock resolver 返回私网 IP，验证拒绝
- **Redirect**：30x 跳公网（接受）、30x 跳 loopback（拒绝）、30x 循环（拒绝 at 5 hops）
- **Size cap**：response 大于 max_bytes → 截断 + 末尾 note；exactly = max_bytes → 不报截断
- **Content-Type**：image/png / application/octet-stream / 空 CT → reject；text/html → 正常处理
- **Timeout**：mock server sleep > 30s → ctx-cancel；agent ctx 取消 → 立即返回
- **HTML 渲染**：典型 docs 站 HTML → 文本输出含正文、不含 script/style 字面值
- **`-race`**：并发 fetch 不应 panic

---

## 七、v1 锁定的接口（v2 兼容契约）

为保证未来扩展是 additive：

1. **工具名 `webfetch`** —— v2 不改名（skill / 文档会引用）
2. **Schema 必填字段 `url`** —— v2 不删；新字段可加
3. **`SEEK_NO_WEBFETCH` 环境变量** —— v2 可加更多 env，但这个名字锁
4. **错误前缀格式 `[webfetch: <category>]`** —— v2 可加新 category，已有的不改

**未锁定**（v2 可改）：max_bytes 默认值、HTML→text 算法细节、Content-Type 白名单具体条目、redirect 上限。

---

## 八、未来可能的扩展（明确不在 v1）

- **`webfetch_head`**：仅拉 HEAD，给模型探测"这个 URL 存在且类型对"
- **简单的 KV cache**：同一 turn 内同一 URL 命中 cache，减少重复抓
- **Per-domain rate limit**：默认每秒 1 次同域
- **`fetched_at` metadata**：在返回 body 头部加时间戳，给模型一个新鲜度感知
- **可选的"摘要模式"**：内置 LLM 调用把 fetched 内容压成摘要再返回（高级用法，违反 v1 简洁原则）

这些都是后续动作，不进 v1。

---

## 九、相关文件

### 待建

- `internal/tools/webfetch/webfetch.go` — Tool 实现
- `internal/tools/webfetch/validate.go` — URL allow/deny + SSRF 防御
- `internal/tools/webfetch/render.go` — HTML → text
- `internal/tools/webfetch/*_test.go` — 单测 + httptest 集成
- `cmd/seek/main.go` — 注册 + env var 识别

### 相关

- [`docs/prd/feature-plan-mode.md`](feature-plan-mode.md) — plan-analyze 是主要消费方；§八 bash 白名单解释了为什么 curl 不能直接放
- [`docs/prd/feature-mcp-client.md`](feature-mcp-client.md) — MCP 是"高级 / 带认证"的网络访问通道；webfetch 是"低级 / 强约束"的通道。两者并存，不互相替代
- [`internal/tools/bash/readonly.go`](../../internal/tools/bash/readonly.go) — 白名单实现 + 设计哲学注释（保守优先原则）
- [`internal/tools/git/`](../../internal/tools/git/) — "read-only by construction" 工具范例；webfetch 沿用同款"不引入新 permission.Kind"决策
- [`internal/tools/read/read.go`](../../internal/tools/read/read.go) — webfetch 是它的远程版本；同款 size cap + LimitReader 模式
