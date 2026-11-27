### bash 3.2 + set -u 把「$var 紧跟多字节字符」并入变量名报 unbound
- **Saw**: `errln "${f}：首行应为 …"` 写成 `"$f：首行应为 …"` 后脚本在 bash 3.2 下整段崩掉：`line 143: f\ufffd: unbound variable`——变量 `f` 在循环里已赋值；夹具一路绿、真数据即崩，因为崩的是错误分支（c_uxznyn 计数脚本实录）
- **Why**: bash 3.2 的变量名解析是字节级的：`$f` 后面紧跟全角冒号 `：`（U+FF1A，首字节 0xEF）时，0xEF 被并进变量名，查到未赋值的 `f\xef`，`set -u` 就地报 unbound 退出。bash 4+ 修了多字节边界，但 macOS 系统 bash 停在 3.2，本仓脚本承诺 3.2 兼容
- **Fix**: 变量后紧跟任何非 ASCII 字符（：，。……）时一律写 `${var}` 大括号形式；并用逐字节扫描（LC_ALL=C 下找 `$name` 后跟高位字节）全仓收口（commit ca6f46a8）
- **Lesson**: 「夹具全绿」对只在失败分支跑到的代码零覆盖——错误路径的字符串拼接也要有负路径自测。中英混排报错 + bash 3.2 + set -u 三因素叠加，缺一不爆，静态扫描是唯一可靠收口法
- **Refs**: `scripts/pitfalls-count.sh:errln 调用点`，commit ca6f46a8
