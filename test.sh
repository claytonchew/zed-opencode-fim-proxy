#!/bin/bash
# Test the proxy with a sample FIM request

PROXY_URL="${PROXY_URL:-http://localhost:11111}"

# Check for API key
if [ -z "$OPENCODE_API_KEY" ]; then
  echo "Error: OPENCODE_API_KEY environment variable is required"
  echo "Usage: OPENCODE_API_KEY=your-key ./test.sh"
  exit 1
fi

echo "Testing proxy at $PROXY_URL"
echo ""

# Test health endpoint
echo "1. Testing health endpoint..."
curl -s "$PROXY_URL/health" | jq .
echo ""

# Test completions with a simple FIM request
echo "2. Testing completions with FIM tokens..."
curl -s -X POST "$PROXY_URL/v1/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OPENCODE_API_KEY" \
  -d '{
    "model": "deepseek-v4-flash",
    "prompt": "<｜fim▁begin｜>def fibonacci(n):\n    <｜fim▁hole｜>\n    return fibonacci(n-1) + fibonacci(n-2)<｜fim▁end｜>",
    "max_tokens": 50,
    "stop": ["\n", "<｜fim▁end｜>"]
  }' | jq .
echo ""

echo "3. Testing with a larger prompt (similar to Zed)..."
curl -s -X POST "$PROXY_URL/v1/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OPENCODE_API_KEY" \
  -d '{
    "model": "deepseek-v4-flash",
    "prompt": "<｜fim▁begin｜>package main\n\nimport \"fmt\"\n\nfunc main() {\n    <｜fim▁hole｜>\n}\n<｜fim▁end｜>",
    "max_tokens": 100,
    "stop": ["\n", "<｜fim▁end｜>", "<｜fim▁begin｜>", "<｜fim▁hole｜>"]
  }' | jq .
