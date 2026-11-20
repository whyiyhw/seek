### 中文注释的 bash 脚本三连坑：`$var（` 拆名、sed BRE `\{`、函数变量互踩（c_ldu44u price-audit.sh 实录）
- **Saw**: price-audit.sh 首版三处独立翻车：① `$d（${days} 天…` 报 `line 153: d<garbled>: unbound variable`——变量名是乱码；② 菜单锚点 `sed -n '/…OpenCodeGoModel\{/,/^}/p'` 在 BSD sed 直接报 RE 错误，提取零命中，自测三个状态全 rc=2；③ 同一脚本两个函数都用裸 `tmp=` 变量名，`audit_root` 收尾 `rm -rf "$tmp"` 把 `live_report` 的临时目录变量覆盖后连着删了
- **Why**: ① C/POSIX locale 下 bash 按字节解析变量名，`$d` 后跟全角字符（UTF-8 多字节）会被拆进名字里——`"…$d（…"` 在 UTF-8 locale 能跑、换个环境就炸，且报错里变量名乱码极难定位；② BRE 里 `\{` 是区间量词 `{n,m}` 的开头，字面 `{` 反而不该转义（`grep -E`/ERE 无此坑）；③ bash 函数里的赋值默认写全局变量，同名即互踩，没有 `local` 就没有隔离
- **Fix**: ① 变量后紧跟全角/非 ASCII 字符一律花括号 `${d}（`；② 锚点改 `\[\]OpenCodeGoModel{`；③ live 侧临时目录改名 `rtmp` 并注释原因（`local` 也行，但显式不同名更防手滑）；另加「提取零命中=完整性错 exit 2」守卫，让锚点漂移第一时间响而不是静默漏检
- **Lesson**: 给带中文注释/中文输出的 bash 脚本立规矩：`$var` 后面挨着非 ASCII 就必须 `${var}`；sed 匹配字面 `{`/`[` 记住 BRE 转义方向与 ERE 相反；多函数脚本里临时目录/循环变量一律唯一命名或 `local`。这类脚本务必带「零命中即报错」的提取守卫——静默空输出是巡检类工具最坏的失败形态
- **Refs**: `scripts/price-audit.sh`（`${d}（` 修法、锚点守卫、rtmp 注释），`internal/pricing/ledger.go` 头注（一行一行的解析契约）
