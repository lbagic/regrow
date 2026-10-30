#!/bin/bash
# Throwaway CI probe: which update test, or which step under it, hangs on the runner.
# Each step runs in its own process group and is killed after $LIMIT seconds.
LIMIT=300
set -m
step() {
	local name=$1; shift
	local start=$(date +%s)
	"$@" >"$RUNNER_TEMP/probe-$name.log" 2>&1 &
	local pid=$! result=ok
	while kill -0 "$pid" 2>/dev/null; do
		if [ $(( $(date +%s) - start )) -ge "$LIMIT" ]; then
			result=HUNG
			kill -TERM -- "-$pid" 2>/dev/null; sleep 2; kill -KILL -- "-$pid" 2>/dev/null
			break
		fi
		sleep 1
	done
	wait "$pid"; local rc=$?
	echo "PROBE $name: $result exit $rc after $(( $(date +%s) - start ))s"
	[ "$result" = ok ] && [ "$rc" -ne 0 ] && tail -5 "$RUNNER_TEMP/probe-$name.log" | sed 's/^/    /'
}
sw_vers; swift --version 2>&1 | head -2; sysctl -n hw.ncpu
work=$RUNNER_TEMP/probe; mkdir -p "$work"
cp /bin/echo "$work/echo"; lipo -archs "$work/echo"
step codesign codesign --force --sign - "$work/echo"
mkdir -p "$work/zip" && ln -s "$work" "$work/zip/link"
step zip sh -c 'cd "$1/zip" && /usr/bin/zip -y -q "$1/x.zip" link' sh "$work"
step ditto-c ditto -c -k --keepParent "$work/zip" "$work/y.zip"
step ditto-x ditto -x -k --noqtn "$work/y.zip" "$work/out"
cat >"$work/ird.swift" <<'SWIFT'
import Foundation
let parent = URL(fileURLWithPath: CommandLine.arguments[1])
let url = try FileManager.default.url(for: .itemReplacementDirectory, in: .userDomainMask, appropriateFor: parent, create: true)
print(url.path)
SWIFT
step ird swift "$work/ird.swift" "$work"
cd app || exit 1
swift build --build-tests 2>&1 | tail -1
for n in 1 2 3; do
	step "full-suite-$n" swift test --skip-build
done
