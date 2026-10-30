#!/bin/bash
# Throwaway CI probe: where the full app suite hangs on the runner.
# Each step runs in its own process group and is killed after $LIMIT seconds;
# a hung step first prints its processes, their stacks and its output so far.
LIMIT=150
set -m
descendants() {
	local p
	echo "$1"
	for p in $(ps -axo pid=,ppid= | awk -v p="$1" '$2==p{print $1}'); do descendants "$p"; done
}
diagnose() {
	local name=$1 root=$2 p
	echo "DIAG $name: processes"
	for p in $(descendants "$root"); do ps -o pid=,ppid=,pgid=,etime=,command= -p "$p"; done
	for p in $(descendants "$root"); do
		echo "DIAG $name: sample of $p $(ps -o comm= -p "$p")"
		sample "$p" 2 -file "$RUNNER_TEMP/sample-$p.txt" >/dev/null 2>&1 ||
			sudo -n sample "$p" 2 -file "$RUNNER_TEMP/sample-$p.txt" >/dev/null 2>&1
		sed -n '/^Call graph:/,/^Total number in stack/p' "$RUNNER_TEMP/sample-$p.txt" 2>/dev/null | cut -c1-220
	done
	echo "DIAG $name: output so far"
	tail -60 "$RUNNER_TEMP/probe-$name.log"
}
step() {
	local name=$1; shift
	local start=$(date +%s)
	"$@" >"$RUNNER_TEMP/probe-$name.log" 2>&1 &
	local pid=$! result=ok
	while kill -0 "$pid" 2>/dev/null; do
		if [ $(( $(date +%s) - start )) -ge "$LIMIT" ]; then
			result=HUNG
			diagnose "$name" "$pid"
			kill -TERM -- "-$pid" 2>/dev/null; sleep 2; kill -KILL -- "-$pid" 2>/dev/null
			break
		fi
		sleep 1
	done
	wait "$pid"; local rc=$?
	echo "PROBE $name: $result exit $rc after $(( $(date +%s) - start ))s"
	[ "$result" = ok ] && tail -3 "$RUNNER_TEMP/probe-$name.log" | sed 's/^/    /'
}
sw_vers; swift --version 2>&1 | head -2; sysctl -n hw.ncpu
cd app || exit 1
swift build --build-tests 2>&1 | tail -1
for n in 1 2 3; do
	step "full-suite-$n" swift test --skip-build
done
