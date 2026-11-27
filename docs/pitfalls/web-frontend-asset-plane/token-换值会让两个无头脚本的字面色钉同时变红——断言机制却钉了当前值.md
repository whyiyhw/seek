### token 换值会让两个无头脚本的字面色钉同时变红——断言机制却钉了当前值
- **Saw**: c_pozbmd 按 dsh 实测把 `--seek-inset` 从 `#14161a` 改成 `#151517` 后，页面测试全绿，`scripts/web-theme-manual.sh` 的 curl 自检（`has "$page" '--seek-inset: #14161a'`）与 `scripts/web-theme-headless.mjs` 的首帧断言（`ff.bg === 'rgb(20, 22, 26)'`）双双红——两个脚本都没提过这个十六进制以外的任何东西。
- **Why**: 两条断言要证的是「主题机制在」（token 块存在、首帧不是白底），但把当时的 token **值**写成了字面量。值一改，断言就变成在测「页面还是昨天那张」，而不是在测机制——与 `webPageSHA256` 同一种形状的钉子，但没有任何东西提示它们存在。
- **Fix**: 两处字面色改到新值，并在就近注释里写明「钉的只是当前 token 值，底色跟着 `--seek-inset` 走」。换值时搜全仓老值：`grep -rn '<old-hex>' scripts/ internal/`。
- **Lesson**: 换 token 值不是纯 CSS 改动——先 `grep` 老色值的**非页面**引用（脚本、无头断言、earlier pitfalls 正文），否则收尾跑验收时才红，且红得像是页面坏了。断言要钉机制就钉机制（读页面里的 token 再比首帧），钉值必须留注释说明它是值。
- **Refs**: `scripts/web-theme-manual.sh:148`；`scripts/web-theme-headless.mjs` 景 2 首帧断言；`internal/controlplane/webfrontend.html` `:root` token 块。
