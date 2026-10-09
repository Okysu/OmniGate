#!/usr/bin/env bash
# 把 Go 覆盖率写入 GitHub Actions Job Summary（本地运行时输出到标准输出）。
# 用法（在 server/ 目录）：../.github/scripts/coverage-report.sh coverage.out
set -uo pipefail

COVERAGE_FILE="${1:-coverage.out}"
SUMMARY="${GITHUB_STEP_SUMMARY:-/dev/stdout}"
emit() { printf '%s\n' "$1" >>"$SUMMARY"; }

emit "## Go 测试覆盖率（PostgreSQL 集成测试）"
emit ""
if [ ! -f "$COVERAGE_FILE" ]; then
  emit "未生成覆盖率文件（测试可能失败或未运行）。"
  exit 0
fi

TOTAL="$(go tool cover -func="$COVERAGE_FILE" 2>/dev/null | awk '/^total:/ {print $3}' || true)"
pkgs_in_profile() {
  awk 'NR>1 { p=$1; sub(/:.*/, "", p); n=split(p, s, "/"); pkg=s[1]; for (i=2; i<n; i++) pkg=pkg"/"s[i]; print pkg }' "$COVERAGE_FILE" | sort -u
}
ALL_LIST="$(mktemp)"; TESTED_LIST="$(mktemp)"
go list ./... 2>/dev/null | sort >"$ALL_LIST"
pkgs_in_profile >"$TESTED_LIST"
emit "**总体覆盖率**：${TOTAL:-N/A}　　**有覆盖数据的包**：$(wc -l <"$TESTED_LIST" | tr -d ' ') / $(wc -l <"$ALL_LIST" | tr -d ' ')"
emit ""
emit "| 包 | 覆盖率 | 覆盖 / 总语句 |"
emit "| --- | ---: | ---: |"
# 按包聚合语句数，覆盖率降序；\140 是反引号（避免 awk 字面量转义告警）。
awk 'NR>1 {
  pathloc=$1; stmts=$2+0; count=$3+0
  pos=0; for (i=length(pathloc); i>=1; i--) if (substr(pathloc,i,1)==":") { pos=i; break }
  n=split(substr(pathloc,1,pos-1), s, "/"); pkg=s[1]; for (j=2; j<n; j++) pkg=pkg"/"s[j]
  tot[pkg]+=stmts; if (count>0) cov[pkg]+=stmts
}
END { for (p in tot) printf "%s|%.1f%%|%d/%d\n", p, (tot[p]>0 ? cov[p]*100/tot[p] : 0), cov[p], tot[p] }' "$COVERAGE_FILE" \
  | sort -t'|' -k2 -rn \
  | awk -F'|' 'BEGIN { bt = "\140" } { printf "| %s%s%s | %s | %s |\n", bt, $1, bt, $2, $3 }' >>"$SUMMARY"
emit ""
emit "<details><summary>没有覆盖数据的包</summary>"
emit ""
comm -23 "$ALL_LIST" "$TESTED_LIST" | awk 'BEGIN { bt = "\140" } { printf "- %s%s%s\n", bt, $1, bt }' >>"$SUMMARY"
emit ""
emit "</details>"
rm -f "$ALL_LIST" "$TESTED_LIST"
