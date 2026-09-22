#!/usr/bin/env bash
# 本地编译脚本：把 cmd/ 下除 gen 以外的命令编译到 tmp/ 下，并保持同名目录结构
#   cmd/consumer -> tmp/consumer/consumer
#   cmd/media    -> tmp/media/media
#
# 用法:
#   ./build.sh                # 编译全部（排除 gen）
#   ./build.sh consumer media # 只编译指定命令
#   RACE=1 ./build.sh         # 额外开启 -race
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CMD_DIR="$ROOT/cmd"
OUT_DIR="$ROOT/tmp"

# 排除的目录（不编译）
EXCLUDE=("gen")

# 收集 cmd 下的一级子目录作为编译目标
targets=()
for d in "$CMD_DIR"/*/; do
	[ -d "$d" ] || continue
	name="$(basename "$d")"
	skip=0
	for e in "${EXCLUDE[@]}"; do
		[ "$name" = "$e" ] && skip=1
	done
	[ "$skip" = 1 ] && continue
	targets+=("$name")
done

# 传参时只编译指定的目标
if [ "$#" -gt 0 ]; then
	selected=()
	for want in "$@"; do
		found=0
		for t in "${targets[@]}"; do
			[ "$t" = "$want" ] && found=1
		done
		if [ "$found" = 0 ]; then
			echo "错误: cmd/$want 不存在或已被排除（排除: ${EXCLUDE[*]}）" >&2
			exit 1
		fi
		selected+=("$want")
	done
	targets=("${selected[@]}")
fi

if [ "${#targets[@]}" -eq 0 ]; then
	echo "错误: cmd/ 下没有可编译的目标" >&2
	exit 1
fi

build_flags=()
[ "${RACE:-0}" = "1" ] && build_flags+=("-race")

for name in "${targets[@]}"; do
	out="$OUT_DIR/$name/$name"
	mkdir -p "$(dirname "$out")"
	echo "==> 编译 cmd/$name -> ${out#"$ROOT"/}"
	(cd "$ROOT" && go build "${build_flags[@]}" -o "$out" "./cmd/$name")
done

echo "完成: 共 ${#targets[@]} 个目标，输出目录 ${OUT_DIR#"$ROOT"/}/"
