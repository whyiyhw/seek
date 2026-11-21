#!/bin/bash
# price-audit.sh — 价卡/模型目录漂移巡检（c_ldu44u，2026-11-19）
#
# 背景：docs/pitfalls.md 六条同族坑——退役 id 静默应答价卡错 4.4×、别名
# 日落孤儿引用×2、跨币种幻影差价、工具族价格离开价格页、峰谷窗口翻转
# ——共同根因是核价日期只活在注释散文里，漂移的唯一信号是人肉记忆。
# 本脚本对 internal/pricing/ledger.go（verified-on 台账）做两项检查：
#
#   检查① 过期：某行 VerifiedOn 距今 > 90 天（STALE_DAYS）→ 报告项
#   检查② 孤儿：代码锚点登记处引用了台账外的模型 id → 报告项
#     锚点三处（退役/日落坑的实发地，docs/pitfalls.md :574/:617）：
#       - internal/budget/budget.go   contextLimits keys（模型上下文表）
#       - internal/tui/commands.go    knownModelsForProvider picker 字面量
#       - internal/pricing/pricing.go OpenCodeGoModels 菜单字面量
#       （常量引用的菜单项由 contextLimits 锚覆盖；锚点提取零命中=脚本
#        完整性错误，见退出码语义）
#     豁免表：llm 二线车道 6 个 M6 id（无 DeepSeek/GLM 价源，防御性成本
#     显示走 unknown-model fallback 是既定设计）——逐项注理由。
#
# 退出码语义（报告不拦构建）：
#   0 = 巡检完成（有发现也 exit 0——发现是报告内容，不是闸）；
#   2 = 脚本完整性错误（台账不可解析 / 日期非法 / 锚点提取零命中）——
#       这类是巡检工具瞎了，必须响；
#   自测模式 0/1 = 三态断言全过 / 有断言失败。
#
# 用法：
#   bash scripts/price-audit.sh                  # 裸跑=确定性自测（三态 fixture）
#   bash scripts/price-audit.sh --selftest       # 同上
#   bash scripts/price-audit.sh --audit ROOT     # 对任意根跑两检查（fixture 用）
#   bash scripts/price-audit.sh --live [--out F] # 巡检本仓：两检查+抓取台账各
#                                                # SourceURL 只读取证，产出
#                                                # markdown 报告（stdout 或 F）
#
# 为什么人工触发、不进 cron（机制预算，c_ldu44u 验收④）：
#   价页/目录的变更节奏是周到月级，90 天台账阈值对应的巡检节奏是季度级
#   ——日级 cron 只会产出没人读的重复报告，等于把「人肉记日期」换成
#   「人肉忽略邮件」；报告型检查的价值恰恰在人读那一下（比对页内数字、
#   决定是否改卡+bump 台账），自动化掉这一步就退化成 always-green 噪音
#   或 always-red 闸，两者都违背「输出报告不拦构建」。巡检还依赖对厂商
#   页的外呼（JS 渲染/网络抖动会造成取证失败），不适合无人值守定时跑。
#   机制健康由裸跑自测守护（selftest 发现制零登记进名单）。
set -u

STALE_DAYS=90

usage() {
  cat >&2 <<'EOF'
用法：
  bash scripts/price-audit.sh                  裸跑=确定性自测（三态 fixture）
  bash scripts/price-audit.sh --selftest       同上
  bash scripts/price-audit.sh --audit ROOT     对任意根跑两检查
  bash scripts/price-audit.sh --live [--out F] 巡检本仓并出 markdown 报告
EOF
  exit 2
}

# ---- 日期 shim：BSD(macOS) 与 GNU date 各一支 -------------------------------
to_epoch() { # YYYY-MM-DD -> UTC 午夜 epoch；非法输入返回非零
  case $1 in
    [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]) ;;
    *) return 1 ;;
  esac
  if date -j -f %Y-%m-%d "$1" +%s >/dev/null 2>&1; then
    TZ=UTC date -j -f %Y-%m-%d "$1" +%s 2>/dev/null
  else
    date -u -d "$1" +%s 2>/dev/null
  fi
}

days_ago_fmt() { # N -> N 天前的 YYYY-MM-DD
  if date -v-0d +%F >/dev/null 2>&1; then
    date -v-"$1"d +%F
  else
    date -d "$1 days ago" +%F
  fi
}

# ---- 台账解析：格式契约见 ledger.go 头注，与 ledger_test.go 同源 ------------
# （sed -E + @ 分隔符：BSD/GNU 通用；BWK awk 无三参 match，不用 awk 捕获）
ledger_entries() { # ROOT -> 逐行 "id|date|url|currency"
  sed -nE 's@^[[:space:]]*"([^"]+)":[[:space:]]*\{VerifiedOn:[[:space:]]*"([0-9]{4}-[0-9]{2}-[0-9]{2})",[[:space:]]*SourceURL:[[:space:]]*"(https://[^"]+)",[[:space:]]*Currency:[[:space:]]*"(USD|CNY)"\},[[:space:]]*$@\1|\2|\3|\4@p' \
    "$1/internal/pricing/ledger.go" 2>/dev/null
}

die_integrity() {
  echo "price-audit: [完整性] $*" >&2
  exit 2
}

# ---- 豁免表：豁免 = 有理由的已知不定价，不是漏网 ---------------------------
exception_reason() {
  case $1 in
    claude-3-5-sonnet-20241022|claude-sonnet-4-20250514)
      echo "llm 二线车道 anthropic 档：无 DeepSeek/GLM 价源，防御性成本显示走 unknown-model fallback（M6 既定）" ;;
    gpt-4o|gpt-4o-mini)
      echo "llm 二线车道 openai 档：同上" ;;
    gemini-2.0-flash|gemini-1.5-pro)
      echo "llm 二线车道 gemini 档：同上" ;;
    *) return 1 ;;
  esac
}

# ---- 锚点提取：零命中 = 锚点形状漂移，巡检已瞎，必须响 ---------------------
# 输出 "id@来源" 逐行；出错走 stderr + exit 2（调用方用 || 捕获）。
anchor_ids() { # ROOT 锚点名(budget|picker|menu)
  root=$1; which=$2
  case $which in
    budget)
      f=$root/internal/budget/budget.go
      [ -f "$f" ] || die_integrity "锚点文件缺失：$f"
      ids=$(sed -n '/var contextLimits = map/,/^}/p' "$f" \
        | grep -E '^[[:space:]]*"[^"]+":' | sed -E 's/^[[:space:]]*"([^"]+)".*/\1/')
      src=internal/budget/budget.go:contextLimits ;;
    picker)
      f=$root/internal/tui/commands.go
      [ -f "$f" ] || die_integrity "锚点文件缺失：$f"
      ids=$(sed -n '/^func knownModelsForProvider/,/^func /p' "$f" \
        | grep -oE '\{"[a-zA-Z0-9][^"]*", "' | sed -E 's/^\{"([^"]+)".*/\1/')
      src=internal/tui/commands.go:knownModelsForProvider ;;
    menu)
      f=$root/internal/pricing/pricing.go
      [ -f "$f" ] || die_integrity "锚点文件缺失：$f"
      ids=$(sed -n '/var OpenCodeGoModels = \[\]OpenCodeGoModel{/,/^}/p' "$f" \
        | grep -oE '\{"[a-zA-Z0-9][^"]*", "' | sed -E 's/^\{"([^"]+)".*/\1/')
      src=internal/pricing/pricing.go:OpenCodeGoModels ;;
    *) die_integrity "未知锚点：$which" ;;
  esac
  [ -n "$ids" ] || die_integrity "锚点 $src 提取零命中——源形状漂移，巡检已瞎，先修提取再巡检"
  echo "$ids" | while read -r i; do echo "$i@$src"; done
}

# ---- 两检查本体：findings 走 stdout，永远不置非零 --------------------------
audit_root() { # ROOT
  root=$1
  [ -f "$root/internal/pricing/ledger.go" ] || die_integrity "台账缺失：$root/internal/pricing/ledger.go"
  entries=$(ledger_entries "$root")
  [ -n "$entries" ] || die_integrity "台账零行可解析——一行一行的格式契约破了（见 ledger.go 头注）"

  # 日期先全量验合法（非法=完整性错，主 shell 里响，不进子 shell）
  bad=$(echo "$entries" | while IFS='|' read -r id d rest; do
    to_epoch "$d" >/dev/null || echo "${id}（${d}）"
  done)
  [ -z "$bad" ] || die_integrity "台账 VerifiedOn 非法日期：$bad"

  tmp=$(mktemp -d) || die_integrity "mktemp 失败"

  # 检查① 过期
  today=$(date -u +%s)
  echo "$entries" | while IFS='|' read -r id d url cur; do
    ep=$(to_epoch "$d")
    days=$(( (today - ep) / 86400 ))
    if [ "$days" -gt "$STALE_DAYS" ]; then
      echo "[stale] $id verified ${d}（${days} 天 > ${STALE_DAYS}）source $url"
    fi
  done > "$tmp/findings"

  # 检查② 孤儿（台账模型 id = 全部行 − tool:* 行）
  echo "$entries" | cut -d'|' -f1 | grep -v '^tool:' | sort -u > "$tmp/ledger_ids"
  : > "$tmp/anchors"
  for a in budget picker menu; do
    if ! anchor_ids "$root" "$a" >> "$tmp/anchors" 2> "$tmp/aerr"; then
      cat "$tmp/aerr" >&2
      rm -rf "$tmp"
      exit 2
    fi
  done
  sort -u "$tmp/anchors" | while IFS='@' read -r id src; do
    if ! grep -Fxq "$id" "$tmp/ledger_ids" && ! exception_reason "$id" >/dev/null; then
      echo "[orphan] ${id}（${src} 引用，台账无此行，豁免表无此 id）"
    fi
  done >> "$tmp/findings"

  stale_n=$(grep -c '^\[stale\]' "$tmp/findings" || true)
  orphan_n=$(grep -c '^\[orphan\]' "$tmp/findings" || true)
  cat "$tmp/findings"
  echo "# 巡检完成：发现 ${stale_n} 过期 / ${orphan_n} 孤儿（阈值 ${STALE_DAYS} 天；发现=报告项，不拦构建）"
  rm -rf "$tmp"
}

# ---- --live：两检查 + 台账各 SourceURL 只读取证 → markdown 报告 ------------
live_report() { # out_file（空=stdout）
  root=$(cd "$(dirname "$0")/.." && pwd)
  out=$1
  rtmp=$(mktemp -d) || die_integrity "mktemp 失败"
  # 注意：rtmp 独立命名——audit_root 内部的 tmp 是全局名，重名会被它删掉
  if ! audit_root "$root" > "$rtmp/audit"; then
    rm -rf "$rtmp"
    exit 2
  fi
  today=$(date -u +%F)
  n_entries=$(ledger_entries "$root" | wc -l | tr -d ' ')

  # 去重源抓取：public GET、只读、落临时目录做证据（失败也是证据，不响）
  ledger_entries "$root" | cut -d'|' -f3 | sort -u > "$rtmp/urls"
  : > "$rtmp/evidence"
  i=0
  while read -r url; do
    i=$((i + 1))
    code=$(curl -sS -L --max-time 30 -o "$rtmp/src$i" -w '%{http_code}' "$url" 2> "$rtmp/src$i.err")
    if [ ! -s "$rtmp/src$i" ]; then
      code="CURL-FAIL(${code:-无响应})"
    fi
    bytes=0
    [ -f "$rtmp/src$i" ] && bytes=$(wc -c < "$rtmp/src$i" | tr -d ' ')
    sha=$(shasum -a 256 "$rtmp/src$i" 2>/dev/null | awk '{print $1}')
    [ -n "$sha" ] || sha=$(sha256sum "$rtmp/src$i" 2>/dev/null | awk '{print $1}')
    [ -n "$sha" ] || sha=-
    echo "$url|$code|$bytes|$sha" >> "$rtmp/evidence"
  done < "$rtmp/urls"

  {
    echo "# 价格台账巡检报告 · ${today}"
    echo
    echo "- 触发：人工（manual）——c_ldu44u 巡检脚本首跑；不进 cron，理由见 scripts/price-audit.sh 头注"
    echo "- 只读声明：源均为 public GET，本次运行唯一写入 = 本报告；台账/代码零改动"
    echo "- 改卡流程：人读「源抓取证据」比对页内数字 → 确认后手改价卡并 bump 台账 VerifiedOn（脚本永不改台账）"
    echo
    echo "## 两检查结果"
    echo
    echo '```'
    cat "$rtmp/audit"
    echo '```'
    echo
    echo "## 台账（${n_entries} 行）"
    echo
    echo "| id | verified-on | 币种 | source |"
    echo "|---|---|---|---|"
    ledger_entries "$root" | sort | while IFS='|' read -r id d url cur; do
      echo "| ${id} | ${d} | ${cur} | ${url} |"
    done
    echo
    echo "## 源抓取证据（$(wc -l < "$rtmp/evidence" | tr -d ' ') 个去重源）"
    echo
    echo "| http | bytes | sha256 | url |"
    echo "|---|---|---|---|"
    while IFS='|' read -r url code bytes sha; do
      echo "| ${code} | ${bytes} | \`${sha}\` | ${url} |"
    done < "$rtmp/evidence"
    echo
    echo "## 人工比对区（巡检官员填：页内数字 vs 台账对应卡）"
    echo
    echo "- （待填：逐源记录页内现价，与 internal/pricing 价卡比对；JS 壳页面如实记「curl 不可读，需浏览器」）"
  } > "$rtmp/report.md"
  if [ -n "$out" ]; then
    cp "$rtmp/report.md" "$out" || { rm -rf "$rtmp"; exit 2; }
    echo "报告已写：$out"
  else
    cat "$rtmp/report.md"
  fi
  rm -rf "$rtmp"
}

# ---- 自测：三态 fixture 各一断言（过期/孤儿/正常）+ 完整性负例 -------------
make_fixture() { # dir state(normal|expired|orphan)
  fx=$1; state=$2
  fresh=$(days_ago_fmt 1)
  old=$(days_ago_fmt 91)
  [ "$state" = expired ] && flash_date=$old || flash_date=$fresh
  ghost=""
  [ "$state" = orphan ] && ghost='	"ghost-model": 1_000_000,'
  mkdir -p "$fx/internal/pricing" "$fx/internal/budget" "$fx/internal/tui"
  cat > "$fx/internal/pricing/ledger.go" <<EOF
var verifiedOn = map[string]verification{
	"deepseek-flash": {VerifiedOn: "${flash_date}", SourceURL: "https://example.com/pricing", Currency: "USD"},
	"ghost-free":     {VerifiedOn: "${fresh}", SourceURL: "https://example.com/pricing", Currency: "USD"},
}
EOF
  cat > "$fx/internal/budget/budget.go" <<EOF
var contextLimits = map[string]int{
	"deepseek-flash": 1_000_000,
	"gemini-2.0-flash": 1_000_000,
${ghost}
}
EOF
  cat > "$fx/internal/tui/commands.go" <<EOF
func knownModelsForProvider(providerName string) []modelChoice {
	return []modelChoice{
		{"deepseek-flash", "DeepSeek V4.1-Flash (default)"},
		{"gemini-2.0-flash", "Gemini 2.0 Flash"},
	}
}
func other() {}
EOF
  cat > "$fx/internal/pricing/pricing.go" <<EOF
var OpenCodeGoModels = []OpenCodeGoModel{
	{"deepseek-flash", "lane default"},
	{"ghost-free", "free row"},
}
EOF
}

selftest() {
  pass=0; fail=0
  assert_contains() { # 名称 内容 子串
    if printf '%s' "$2" | grep -Fq -- "$3"; then
      echo "  ok: $1"; pass=$((pass + 1))
    else
      echo "  FAIL: ${1}（输出未见：${3}）" >&2; fail=$((fail + 1))
    fi
  }
  assert_rc() { # 名称 实际rc 期望rc
    if [ "$2" -eq "$3" ]; then
      echo "  ok: $1"; pass=$((pass + 1))
    else
      echo "  FAIL: ${1}（exit=${2}，期望 ${3}）" >&2; fail=$((fail + 1))
    fi
  }
  tmp=$(mktemp -d) || die_integrity "mktemp 失败"

  echo "自测① 正常态：无任何发现，exit 0"
  make_fixture "$tmp/normal" normal
  out=$(bash "$0" --audit "$tmp/normal"); rc=$?
  assert_contains "正常态汇总 0 过期" "$out" "发现 0 过期"
  assert_contains "正常态汇总 0 孤儿" "$out" "0 孤儿"
  if printf '%s' "$out" | grep -Fq '[stale]'; then
    echo "  FAIL: 正常态不应有 [stale]" >&2; fail=$((fail + 1))
  else
    echo "  ok: 正常态无 [stale]/[orphan] 行"; pass=$((pass + 1))
  fi
  assert_rc "正常态 exit 0" "$rc" 0

  echo "自测② 过期态：91 天前的行报 [stale]，且不拦（exit 0）"
  make_fixture "$tmp/expired" expired
  out=$(bash "$0" --audit "$tmp/expired"); rc=$?
  assert_contains "过期态报 [stale] deepseek-flash" "$out" "[stale] deepseek-flash"
  assert_rc "过期态仍是 exit 0（报告不拦构建）" "$rc" 0

  echo "自测③ 孤儿态：锚点引用台账外 id 报 [orphan]，且不拦（exit 0）"
  make_fixture "$tmp/orphan" orphan
  out=$(bash "$0" --audit "$tmp/orphan"); rc=$?
  assert_contains "孤儿态报 [orphan] ghost-model" "$out" "[orphan] ghost-model"
  assert_rc "孤儿态仍是 exit 0（报告不拦构建）" "$rc" 0

  echo "自测④ 完整性负例：台账整文件不可解析 → exit 2 响"
  make_fixture "$tmp/broken" normal
  echo 'var verifiedOn = 改坏' > "$tmp/broken/internal/pricing/ledger.go"
  out=$(bash "$0" --audit "$tmp/broken" 2>&1); rc=$?
  assert_rc "坏台账 exit 2（工具瞎了必须响）" "$rc" 2

  rm -rf "$tmp"
  echo "自测：${pass} 过 / ${fail} 败"
  [ $fail -eq 0 ]
}

# ---- 入口 -------------------------------------------------------------------
mode=${1:---selftest}
case $mode in
  --selftest) [ $# -le 1 ] || usage; selftest ;;
  --audit)    [ $# -eq 2 ] || usage; audit_root "$2" ;;
  --live)
    case ${2:-} in
      --out) [ $# -eq 3 ] || usage; live_report "$3" ;;
      "")    live_report "" ;;
      *)     usage ;;
    esac ;;
  -h|--help)  usage ;;
  *)          usage ;;
esac
