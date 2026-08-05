#!/usr/bin/env bash
set -euo pipefail
exe="$1"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
in="$tmp/in.jsonl"
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"ci","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  '{"jsonrpc":"2.0","id":3,"method":"ping","params":{}}' > "$in"
out="$tmp/out.txt"
err="$tmp/err.txt"
"$exe" --mcp < "$in" > "$out" 2> "$err" &
pid=$!
# GNU timeout is not guaranteed on macOS; emulate it with a background watchdog.
{
  sleep 30
  kill "$pid" 2>/dev/null || true
} &
watchdog=$!
if wait "$pid"; then
  code=0
else
  code=$?
fi
kill "$watchdog" 2>/dev/null || true
wait "$watchdog" 2>/dev/null || true
if [ "$code" -ne 0 ]; then
  if [ "$code" -eq 143 ]; then
    echo "MCP process timed out"
  else
    echo "MCP exit $code: $(cat "$err")"
  fi
  exit 1
fi
if [ -s "$err" ]; then echo "MCP stderr not empty: $(cat "$err")"; exit 1; fi
lines=$(grep -c . "$out" || true)
if [ "$lines" -ne 3 ]; then echo "expected 3 responses, got $lines"; exit 1; fi
proto=$(sed -n '1p' "$out" | grep -o '"protocolVersion":"[^"]*"')
if [ "$proto" != '"protocolVersion":"2024-11-05"' ]; then echo "protocol mismatch: $proto"; exit 1; fi
tools=$(sed -n '2p' "$out" | grep -o '"tools":\[' | head -1)
if [ -z "$tools" ]; then echo "missing tools array"; exit 1; fi
tool_count=$(sed -n '2p' "$out" | grep -o '"name"' | wc -l | tr -d ' ')
if [ "$tool_count" -ne "10" ]; then echo "expected 10 tools, got $tool_count"; exit 1; fi
echo "MCP smoke OK"
