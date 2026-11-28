### ACP 的 ask_user 不是"没有人类"，是"没接协议"——而协议早就有
- **Saw**: ACP 车道问选择题，工具结果永远 `ErrDisabled`「rephrase as a plain-text question」；`/tmp/seek-acp.log` 几千轮会话证明客户端在线。
- **Why**: ①`askPolicy` 出生 `ModeDisabled`，仅 TUI `SetMode(ModeAsk)`（`cmd/seek/main.go:1916`），`runACP` 缺这行（v0.13.0 反转默认值，对 print/rpc/goal 对、对 ACP 错）。②ACP schema 早有 `elicitation/create`（form 模式），由 `clientCapabilities.elicitation.form` 门控，当时误以为没有。
- **Fix**: `internal/acp/elicitation.go`（wire+解析+能力闸）+ `cmd/seek/acp_askuser.go`（`askuser.Question`→form schema，回答→`Answer`）+ `initialize` 门控：申报 `elicitation.form` 才 `SetMode(ModeAsk)`；未申报逐字节旧行为。
- **Lesson**: 该不该交互看 `initialize` 的 `clientCapabilities`，不看车道名；sub-capability 对象在场即支持（`{"form":{}}` 开，`null`/省略关），Go 侧须解 `*struct{}` 而非 `bool`——bool 把「未申报」与「声明为关」合并。
- **Refs**: `internal/acp/elicitation.go`、`cmd/seek/acp_askuser.go`、`internal/acp/server.go`、`docs/guide-zed.md` §5.7、`@agentclientprotocol/sdk` 1.9.0 `schema/types.gen.d.ts`（`CreateElicitationRequest`/`ElicitationCapabilities`）、旁证 zcode bridge `zcode-acp-server/dist/server.js:1313`
