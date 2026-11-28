### token 换值会让两个无头脚本的字面色钉同时变红——断言机制却钉了当前值
- **Saw**: c_pozbmd 按 dsh 实测把 `--seek-inset` 从 `#14161a` 改成 `#151517`：页面测试全绿，两处无头断言同时红——`scripts/web-theme-manual.sh` 的 curl 自检（`has "$page" '--seek-inset: #14161a'`）、`scripts/web-theme-headless.mjs` 的首帧断言（`ff.bg === 'rgb(20, 22, 26)'`）；两者都只提这个 hex。
- **Why**: 两条断言要证「主题机制在」（token 块存在、首帧非白底），却把 token **值**钉成字面量：值一改就成了测「页面还是昨天那张」——`webPageSHA256` 同一种钉子，且无物提示其存在。
- **Fix**: 两处字面色改新值，就近注释「钉的是当前 token 值，底色跟着 `--seek-inset` 走」。换值时搜全仓老值：`grep -rn '<old-hex>' scripts/ internal/`。
- **Lesson**: 换 token 值不是纯 CSS 改动——先 `grep` 老色值的**非页面**引用（脚本、无头断言、earlier pitfalls 正文），否则收尾验收才红，红得像页面坏了。钉机制就钉机制（读页面 token 再比首帧），钉值须注释说明。
- **Refs**: `scripts/web-theme-manual.sh:148`；`scripts/web-theme-headless.mjs` 景 2 首帧断言；`internal/controlplane/webfrontend.html` `:root` token 块。
