### acp-smoke 断言在 wire 契约变更后悄悄过时（sessionId=项目名 → 持久 id）
- **Saw**: `scripts/acp-smoke-test.sh` 在 base 提交上就是红的：断言 `session/new` 回 `sessionId=="smoke-ws"`（项目名），而 2026-11-19 session/load 落地后真实回的是持久 transcript id（`20261121-…`）。两天里没人发现——这个 smoke 只在按需/验证车道跑，平时 CI 只有 `go test`，而 go 测试早就改成断言持久 id 了。
- **Why**: wire 契约（sessionId 语义）变了，改了 Go 单测却没改真二进制 smoke 的断言；smoke 没进任何常规闸，红着也无人看见。
- **Fix**: c_chzxgg 顺手修断言（形状匹配 `[0-9]{8}-[0-9]{6}-[0-9a-f]{16}` + 与 stderr anchor 行 `id=` 交叉核对），并把教训写进脚本注释：改 wire 契约的同一个 change 必须带上它的真二进制断言。
- **Lesson**: 一个契约有两处真相（单测 + smoke 脚本）时，改契约只改一处必然留下红着的另一处；不常跑的脚本红着不等于没人欠账。发现式 selftest（c_aqbdaz）让脚本能被跑到的门槛变低了，但"跑不跑"仍是人的动作。
- **Refs**: `scripts/acp-smoke-test.sh`（§2 注释）、`cmd/seek/main.go` NewSession（持久 id 契约）。
