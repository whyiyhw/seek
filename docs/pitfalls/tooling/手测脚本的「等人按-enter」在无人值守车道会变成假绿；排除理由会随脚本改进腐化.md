### 手测脚本的「等人按 Enter」在无人值守车道会变成假绿；排除理由会随脚本改进腐化
- **Saw**: `scripts/selftest.sh` 把 `web-sessions-manual` 排除，理由「等人按 Enter（交互阻塞）」，但脚本早有 Chrome+node 无头分支（跑完断言自己 `exit 0`），条件不全才落 `read -r _`；理由被三轮验证标可疑（c_n63pat/c_hinacy/c_txz6vz，5 卡串行等它）。且 stdin 关闭时 `read` 读 EOF 即过——无凭据绿。
- **Why**: (a) 排除理由描述写下那一刻的脚本形态，无头分支后加、理由没人重读；(b) `read -r _` TTY 上等人、非 TTY 读 EOF 返回 0——同一行两类车道语义相反。
- **Fix**: (a) 移出排除表进发现名单，头注释加「改脚本形态时顺手重读本表」；(b) 人工清单前加 `[ -t 0 ] || exit 3` 点名缺 Chrome/node，绿只由无头断言给。c_gqo32q 同判据收剩余两臂：`web-stream-manual`/`web-md-manual` 补 exit 3 后移出；`web-memory-manual` 保留（无 `*-headless.mjs`，结构性）。`web-stop-manual`（c_5m3fm6）已同法走完。
- **Lesson**: 「阻塞」类排除判据必须有对应非交互断言，否则静默失效；`read` 收尾脚本可能进无人值守车道就先判 `-t 0`；发现制消灭「忘登记」，「忘重判」靠这条纪律。
- **Refs**: `scripts/selftest.sh` 头注释+排除表、`scripts/web-{sessions,stream,md,memory,stop}-manual.sh`
