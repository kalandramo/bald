#!/bin/bash

SERVER_URL="http://127.0.0.1:8999"
HDR_FILE=$(mktemp)

echo "[1] Initialize session over Streamable HTTP..."
curl -s -D "$HDR_FILE" -o /dev/null -X POST "$SERVER_URL/mcp" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc": "2.0","id": 1,"method": "initialize","params": {"protocolVersion": "2025-06-18","capabilities": {},"clientInfo": {"name": "test-mcp","version": "0.1.0"}}}'

SESSION_ID=$(grep -i "^mcp-session-id:" "$HDR_FILE" | awk '{print $2}' | tr -d '\r')
rm -f "$HDR_FILE"

if [ -z "$SESSION_ID" ]; then
  echo "ERROR: no Mcp-Session-Id header in initialize response"
  exit 1
fi
echo "    session: $SESSION_ID"

echo "[2] Send RPC request (tools/list) to: $SERVER_URL/mcp"
RESPONSE=$(curl -s -X POST "$SERVER_URL/mcp" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -d '{"jsonrpc": "2.0","id": 2,"method": "tools/list","params": {}}')

echo "=========================================="
echo "Tools List (Raw JSON):"
# Streamable HTTP 的响应可能是 application/json，也可能以 SSE 帧包装——
# 后者取 "data: " 行还原 JSON 原文。
if echo "$RESPONSE" | grep -q '^data:'; then
  echo "$RESPONSE" | grep '^data:' | sed 's/^data:[[:space:]]*//'
else
  echo "$RESPONSE"
fi
echo "=========================================="
