#!/usr/bin/env bash
# 实测 deepseek API 是否支持图片输入（image_url）
set -u
cd "$(dirname "$0")/.."

KEY=$(grep -E "^FISH_LLM_KEY=" .env | head -1 | cut -d= -f2-)
if [ -z "$KEY" ]; then
  echo "no FISH_LLM_KEY"
  exit 1
fi

curl -s -o /tmp/ds-vision-test.json -w "HTTP %{http_code}\n" https://api.deepseek.com/v1/chat/completions \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-chat",
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "What is in this image? One sentence."},
        {"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="}}
      ]
    }],
    "max_tokens": 50
  }'
echo "=== 响应 ==="
head -c 600 /tmp/ds-vision-test.json
