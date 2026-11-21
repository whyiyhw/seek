### 手测脚本的「等人按 Enter」在无人值守车道会变成假绿；排除理由会随脚本改进腐化
- **Saw**: `scripts/selftest.sh` 把 `web-sessions-manual` 排除在发现名单外，理由是「等人按 Enter（交互阻塞）」——但那个脚本早就有 Chrome+node 无头分支（跑完全部断言、自己 `exit 0`），只有条件不全时才落到 `read -r _`。该理由连续被三轮验证会话标注可疑（c_n63pat 附注 / c_hinacy not-in-lane / c_txz6vz 停卡，5 张卡串行等它）；而真把 fallback 放进车道还有第二个坑：stdin 关掉时 `read` 读空即过 → 无凭据的绿。
- **Why**: 两个独立的腐化。(a) 排除表的理由描述的是脚本「写下理由那一刻」的形态，脚本后来加了无头分支，理由没人重读——判据过时被当成措辞问题，于是一路绕过。(b) `read -r _` 在 TTY 上是「等人」，在非 TTY 上是「读到 EOF 立即返回 0」，同一行代码在两条车道语义相反；靠人工清单收尾的脚本天然没有无人值守语义。
- **Fix**: (a) 移出排除表进发现名单（默认倾向重判，不是润色文案），`selftest.sh` 头注释加「改脚本形态时顺手重读本表」；(b) 人工清单前加 `[ -t 0 ] || exit 3` 并点名缺 Chrome 还是 node——车道报「环境能力缺」，不挂死也不假装绿。绿只由无头断言给出。
- **Lesson**: 排除理由里的「阻塞」类判据要有对应的非交互断言，否则它会静默失效；任何 `read` 收尾的手测脚本，凡是可能进无人值守车道，都必须先判 `-t 0`。发现制把「忘登记」做成不可能，但「忘重判」还得靠这条纪律。
- **Refs**: `scripts/selftest.sh` 头注释 + 排除表、`scripts/web-sessions-manual.sh` 无人值守退路口径、`scripts/web-stream-manual.sh` / `scripts/web-memory-manual.sh`（同族，前者补 exit 3 后可同法移出）；`scripts/web-stop-manual.sh`（c_5m3fm6 已验证同法走完一轮：补 `-t 0` 退 3 + 删排除臂）
