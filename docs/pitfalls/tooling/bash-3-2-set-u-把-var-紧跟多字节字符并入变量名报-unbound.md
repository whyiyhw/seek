### bash 3.2 + set -u 把「$var 紧跟多字节字符」并入变量名报 unbound
- **Saw**: `errln "${f}：首行应为 …"` 一类中英文混排报错行，写成本来的 `"$f：首行应为 …"` 后脚本在 bash 3.2 下整段崩掉：`line 143: f\ufffd: unbound variable`——变量 `f` 明明在循环里赋了值（c_uxznyn 计数脚本实录；自测夹具一路绿、真数据一碰就崩，因为崩的是错误分支）
- **Why**: bash 3.2 的变量名解析是字节级的：`$f` 后面紧跟全角冒号 `：`（U+FF1A，首字节 0xEF）时，0xEF 被并进变量名一起查表，查到 `f\xef` 这个从未赋值的名字，`set -u` 就地报 unbound 退出。bash 4+ 修了多字节边界，但 macOS 系统 bash 停在 3.2，而本仓脚本明确承诺 3.2 兼容
- **Fix**: 变量后面紧跟任何非 ASCII 字符（：，。……）时一律写全 `${var}` 大括号形式；用逐字节扫描（LC_ALL=C 下找 `$name` 后跟高位字节）全仓扫一遍收口，别等崩（commit ca6f46a8）
- **Lesson**: 「夹具全绿」对只在失败分支跑到的代码零覆盖——错误路径的字符串拼接也要有负路径自测。中英混排报错信息 + bash 3.2 + set -u 是三因素叠加坑，缺一不爆，静态扫（而不是等爆）是唯一可靠收口法
- **Refs**: `scripts/pitfalls-count.sh:errln 调用点`，commit ca6f46a8
