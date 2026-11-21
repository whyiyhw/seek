### ACP smoke 探针 id 必须全脚本唯一且前缀不相交——wait_for 是子串 grep
- **Saw**: 合并 main(P0-3) 进 P1-3 分支后，`acp-smoke-test.sh` 里两个探针都用 `rpc 5`，终止帧的「帧先于响应」断言抓到的是图片探针的响应行；且 `wait_for '"id":2'` 会被 id 21 探针的响应行子串匹配而瞬间假通过，SID 提取可能抓空。
- **Why**: 两车道各自扩展同一 smoke 脚本、各自挑了 id 5——git 只在文本重叠处标冲突，语义冲突（id 撞车、全文件 `grep -c` 帧计数被后来的探针污染）不标；`wait_for` 对累积的 OUT 做子串 grep，id 2 是 id 21 的前缀。
- **Fix**: 探针 id 全脚本唯一（图片探针改 6），`wait_for` 模式补尾逗号（`'"id":2,'`——Go JSON 必发 `"id":N,"result"`）；会拉 `_seek_turn_stop` 帧的图片探针挪到「恰一帧」断言之后跑。合并提交 1864803e。
- **Lesson**: 两车道改同一 smoke 合并时，要对**合并后的 id 全集**重查每一处 grep/计数断言，不能只看冲突标记。
- **Refs**: `scripts/acp-smoke-test.sh`；卡 c_4pczu3 重派（上一轮合并即撞这两个文件）。
