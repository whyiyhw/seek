### 手测脚本的「等人按 Enter」在无人值守车道会变成假绿；排除理由会随脚本改进腐化
- **Saw**: `scripts/selftest.sh` 把 `web-sessions-manual` 排除，理由「等人按 Enter（交互阻塞）」——但脚本早有 Chrome+node 无头分支（跑完断言自己 `exit 0`），条件不全才落 `read -r _`；三轮验证都标该理由可疑（c_n63pat/c_hinacy/c_txz6vz，5 卡串行等它）。stdin 关闭时 `read` 读 EOF 即过——无凭据绿。
- **Why**: (a) 理由写的是当时脚本形态，无头分支后加、没人重读；(b) `read -r _` 在 TTY 上等人、非 TTY 读 EOF 返回 0——同一行两类车道语义相反。
- **Fix**: (a) 移出排除表进发现名单，头注释加「改脚本形态时重读本表」；(b) 人工清单前加 `[ -t 0 ] || exit 3` 点名缺 Chrome/node，绿只由无头断言给。c_gqo32q 同判据收剩余两臂：`web-stream-manual`/`web-md-manual` 补 exit 3 后移出；`web-memory-manual` 保留（无 `*-headless.mjs`）。`web-stop-manual`（c_5m3fm6）已同法走完。
- **Lesson**: 「阻塞」类排除判据必须有对应非交互断言，否则静默失效；`read` 收尾的脚本可能进无人值守车道，就先判 `-t 0`。
- **Refs**: `scripts/selftest.sh` 头注释+排除表、`scripts/web-{sessions,stream,md,memory,stop}-manual.sh`
