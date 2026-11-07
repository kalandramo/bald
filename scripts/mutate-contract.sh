#!/usr/bin/env bash
# 变异验证：每次把一处判据改坏，跑目标测试，要求 REAL RED（至少一条用例红），
# 最后按校验和证明源文件字节级还原。单写者：脚本运行期间不提交、不并发。
#
# 用法：bash scripts/mutate-contract.sh <foldkeys|health>
#
# 变异写法约定：替换文本必须让被删表达式里的局部变量仍被引用（用 `&& false` /
# `|| true` 之类的中性化写法），否则会触发 "declared and not used"，得到的不是
# 判据红而是编译红——那是假信号。脚本会显式区分这两类结果。
set -uo pipefail
cd "$(dirname "$0")/.."

SUITE="${1:-foldkeys}"
case "$SUITE" in
foldkeys)
	MODULE_DIR=bconf
	STRAP=TestFoldEnvKeys
	TARGETS="bconf/foldkeys.go"
	;;
health)
	MODULE_DIR=pkg/appkit
	STRAP=TestContractHealth
	TARGETS="pkg/appkit/health.go pkg/appkit/bootstrap.go pkg/appkit/plan_compile.go"
	;;
*) echo "unknown suite: $SUITE"; exit 1 ;;
esac

BACKUP_DIR="$(mktemp -d)"
SHA_LIST=()
FILES=()
for f in $TARGETS; do
	cp "$f" "$BACKUP_DIR/$(echo "$f" | tr / _)"
	SHA_LIST+=("$(shasum -a 256 "$f")")
	FILES+=("$f")
done

restore() {
	for f in "${FILES[@]}"; do cp "$BACKUP_DIR/$(echo "$f" | tr / _)" "$f"; done
}
trap 'restore; rm -rf "$BACKUP_DIR"' EXIT

# run_case <label> <file> <old> <new>
run_case() {
	local label="$1" file="$2" old="$3" new="$4"
	restore
	cp "$BACKUP_DIR/$(echo "$file" | tr / _)" "$file"
	if ! python3 - "$file" "$old" "$new" <<'PY'
import sys
path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
src = open(path).read()
if src.count(old) != 1:
    sys.exit("MUTATION_NOT_APPLIED count=%d" % src.count(old))
open(path, "w").write(src.replace(old, new))
PY
	then
		echo "[$label] SKIP: 变异点没唯一命中（源文本变了，先修脚本）"
		return
	fi
	local out verdict reds hits
	out=$(cd "$MODULE_DIR" && GOWORK=off go test -run "$STRAP" -count=1 . 2>&1)
	if printf '%s\n' "$out" | grep -Eq 'build failed|declared and not used|undefined:|syntax error'; then
		echo "[$label] NOT-COMPILED: $(printf '%s\n' "$out" | grep -Em1 'declared and not used.*|undefined:.*|syntax error.*')"
		return
	fi
	reds=$(printf '%s\n' "$out" | grep -Ec '^ *--- FAIL' || true)
	hits=$(printf '%s\n' "$out" | grep -Eo "${STRAP}_[A-Za-z_]+" | sort -u | tr '\n' ' ')
	verdict=$([[ "$reds" -gt 0 ]] && echo "RED x$reds " || echo "NOT-RED  <== 判据没钉住这条变异")
	echo "[$label] $verdict $hits"
}

case "$SUITE" in
foldkeys)
	run_case "M1 退回唯一链判据（多子节点不折）" bconf/foldkeys.go \
'	sub, ok := cur.(map[string]any)
	if !ok || len(sub) == 0 {
		return false
	}' \
'	sub, ok := cur.(map[string]any)
	if !ok || len(sub) != 1 {
		return false
	}'
	run_case "M2 不掏空词头键（缺自底向上清理）" bconf/foldkeys.go \
'	return len(sub) == 0
}' '	return false
}'
	run_case "M3 一条分支拼不出就放弃整个词头" bconf/foldkeys.go \
'			if foldWalk(byName, m, child, next) {
				delete(sub, ck)
			}
			continue' \
'			if foldWalk(byName, m, child, next) {
				delete(sub, ck)
			}
			return false'
	run_case "M4 取消「规范键已被占用」让位判据" bconf/foldkeys.go \
'		if _, _, taken := lookupKeyFold(m, name); taken {
			continue // 规范键已由另一种形状占据
		}' \
'		if _, _, taken := lookupKeyFold(m, name); taken && false {
			continue
		}'
	run_case "M5 取消 JSON 名占用判据" bconf/foldkeys.go \
'		if _, _, taken := lookupKeyFold(m, fd.JSONName()); taken {
			continue
		}' \
'		if _, _, taken := lookupKeyFold(m, fd.JSONName()); taken && false {
			continue
		}'
	run_case "M6 平级规范名优先被折叠抢走" bconf/foldkeys.go \
'		if _, ok := byName[strings.ToLower(k)]; ok {
			continue // 平级规范名优先
		}' \
'		if _, ok := byName[strings.ToLower(k)]; ok && false {
			continue
		}'
	run_case "M7 折成 JSON 名而非 proto 名" bconf/foldkeys.go \
'		name := string(fd.Name())' '		name := fd.JSONName()'
	run_case "M8 值搬走了却不删被消费的叶子" bconf/foldkeys.go \
'		m[name] = child
		delete(sub, ck)' \
'		m[name] = child'
	run_case "M9 map 字段不再跳过（按 message 下钻）" bconf/foldkeys.go \
'		case fd.IsMap():
			// map 的键由业务自持，不折叠也不下钻。' \
'		case fd.IsMap():
			if mv, ok := v.(map[string]any); ok {
				if cm := fd.MapValue().Message(); cm != nil {
					foldMessage(cm, mv)
				}
			}'
	;;
health)
	run_case "H1 assemble 不做契约映射" pkg/appkit/bootstrap.go \
'	applyContractHealth(spec, cfg)' '	// 变异：装配期不做契约映射'
	run_case "H2 Compile 不做契约映射（预检与启动不同源）" pkg/appkit/plan_compile.go \
'	applyContractHealth(d, cfg)' '	// 变异：计划期不做契约映射'
	run_case "H3 路径映射不看业务表态（契约覆盖业务）" pkg/appkit/health.go \
'	if !d.probeLiveSet {
		if v := app.GetProbeLivePath(); v != "" {
			d.ProbeLivePath = v
		}
	}
	if !d.probeReadySet {
		if v := app.GetProbeReadyPath(); v != "" {
			d.ProbeReadyPath = v
		}
	}' \
'	if v := app.GetProbeLivePath(); v != "" {
		d.ProbeLivePath = v
		_, _ = d.probeLiveSet, d.probeReadySet
	}
	if v := app.GetProbeReadyPath(); v != "" {
		d.ProbeReadyPath = v
	}'
	run_case "H4 间隔映射不看表态" pkg/appkit/health.go \
'	if !d.healthIntervalSet {
		if v := app.GetHealthPollInterval(); v != nil {
			d.HealthInterval = v.AsDuration()
		}
	}' \
'	if v := app.GetHealthPollInterval(); v != nil {
		d.HealthInterval = v.AsDuration()
	}
	_ = d.healthIntervalSet'
	run_case "H5 两个路径字段当一个用（不逐字段独立）" pkg/appkit/health.go \
'		if v := app.GetProbeReadyPath(); v != "" {' \
'		if v := app.GetProbeLivePath(); v != "" {'
	run_case "H6 不校验「字段有值却没有健康源」" pkg/appkit/health.go \
'		if live != "" || ready != "" || interval != nil {' \
'		if live != "" && false || ready != "" && false || interval != nil && false {'
	run_case "H7 放行非正轮询间隔（悄悄回退 2s）" pkg/appkit/health.go \
'	if interval != nil && interval.AsDuration() <= 0 {' \
'	if interval != nil && interval.AsDuration() <= 0 && false {'
	run_case "H8 放行两条探针同路径" pkg/appkit/health.go \
'	if d.ProbeLivePath == d.ProbeReadyPath {' \
'	if d.ProbeLivePath == d.ProbeReadyPath && false {'
	run_case "H9 放行不以 / 开头的探针路径" pkg/appkit/health.go \
'		if !strings.HasPrefix(p.value, "/") {' \
'		if !strings.HasPrefix(p.value, "/") && false {'
	run_case "H10 WithProbePaths 不记表态" pkg/appkit/health.go \
'			s.ProbeLivePath = live
			s.probeLiveSet = true' '			s.ProbeLivePath = live'
	run_case "H11 WithHealth 的默认值算表态" pkg/appkit/health.go \
'		s.ProbeLivePath = DefaultHealthPath
		s.ProbeReadyPath = DefaultReadyPath' \
'		s.ProbeLivePath = DefaultHealthPath
		s.ProbeReadyPath = DefaultReadyPath
		s.probeLiveSet, s.probeReadySet = true, true'
	;;
esac

restore
echo "== 还原校验 =="
rc=0
i=0
for f in "${FILES[@]}"; do
	now="$(shasum -a 256 "$f")"
	if [[ "$now" == "${SHA_LIST[$i]}" ]]; then
		echo "OK: $f 字节级还原"
	else
		echo "FAIL: $f 还原不一致（${SHA_LIST[$i]} -> $now）"; rc=1
	fi
	i=$((i + 1))
done
exit "$rc"
