### 中文注释的 bash 脚本三连坑：`$var（` 拆名、sed BRE `\{`、函数变量互踩（c_ldu44u price-audit.sh 实录）
- **Saw**: price-audit.sh 首版三连坑：① `$d（${days} 天…` 报 `line 153: d<garbled>: unbound variable`；② 锚点 `sed -n '/…OpenCodeGoModel\{/,/^}/p'` 在 BSD sed 报 RE 错、零命中，自测三状态 rc=2；③ 两函数共用裸 `tmp=`，`audit_root` 收尾 `rm -rf "$tmp"` 连删 `live_report` 的临时目录。
- **Why**: ① C/POSIX locale 下 bash 按字节解析变量名，`$d` 后的全角字符被拆进名字（UTF-8 locale 能跑，换环境即炸）；② BRE 里 `\{` 是区间量词 `{n,m}` 开头，字面 `{` 不该转义（ERE 无此坑）；③ bash 函数赋值默认全局，同名互踩。
- **Fix**: ① 非 ASCII 紧跟变量用 `${d}（`；② 锚点改 `\[\]OpenCodeGoModel{`；③ live 侧临时目录改名 `rtmp`（`local` 亦可）；另加零命中=完整性错 exit 2 守卫。
- **Lesson**: `$var` 后挨着非 ASCII 必须 `${var}`；sed 字面 `{`/`[`：BRE 转义方向与 ERE 相反；多函数脚本临时变量唯一命名或 `local`；巡检工具带零命中报错守卫——静默空输出是最坏失败。
- **Refs**: `scripts/price-audit.sh`（`${d}（` 修法/锚点守卫/rtmp）、`internal/pricing/ledger.go` 头注
