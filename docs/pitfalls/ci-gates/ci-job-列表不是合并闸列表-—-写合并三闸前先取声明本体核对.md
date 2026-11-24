### CI job 列表不是合并闸列表——写「合并三闸」前先取声明本体核对

- **Saw**: c_5qrafk 第一轮把「合并三闸」写成 ci.yml 的一个 lint job + doc-budgets + web-gate，验证会话判返工：该 lint job 只存在于 `.github/workflows/ci.yml` 的 job 列表（约 :57），合并点实际跑的是 `doc-budgets / pitfalls-count / web-gate`。
- **Why**: 两个列表语义相邻但权威来源不同——CI job 由 ci.yml 定义，合并闸由 butler 的 `.butler/merge-gates.json` 声明（machine-local、克隆里通常没有，故工作树里读不到，容易就近拿 ci.yml 反推）。
- **Fix**: prose 三处（`scripts/selftest.sh` 头注释与 web-gate 排除臂、`docs/pitfalls/tooling/…not-in-lane.md` 的 Fix 行）改为 `pitfalls-count`（c_5qrafk 返工第 1 轮修正提交）。
- **Lesson**: 合并闸名单的唯一权威是声明本体：`git show 73e17464:.butler/merge-gates.json`（该文件曾一次被跟踪，可作证据锚）。CI job 增删与合并闸无关，别从 job 列表反推闸名单。
- **Refs**: `.github/workflows/ci.yml`、`.butler/merge-gates.json` @ 73e17464、cards c_5qrafk / c_vxh4ro
