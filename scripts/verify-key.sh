#!/usr/bin/env bash
# 验证 .env 中的 embedding key 对智谱 API 真实有效（只输出结果，不输出 key）
set -u
cd "$(dirname "$0")/.."
KEY=$(grep '^FISH_EMBEDDING_KEY=' .env | head -1 | cut -d= -f2- | tr -d '"')
if [ -z "$KEY" ]; then
  echo "FISH_EMBEDDING_KEY 未配置"
  exit 1
fi
echo "key 长度: ${#KEY}"
export FISH_EMBEDDING_KEY="$KEY"
mkdir -p cmd/tmp-keycheck
cat > cmd/tmp-keycheck/main.go <<'GOEOF'
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"RaiseMyFish/internal/embedding"
)

func main() {
	client, err := embedding.New(embedding.Options{
		URL:       "https://open.bigmodel.cn/api/paas/v4/embeddings",
		Model:     "embedding-3",
		APIKey:    os.Getenv("FISH_EMBEDDING_KEY"),
		Dimension: 1024,
		BatchSize: 1,
		Timeout:   20 * time.Second,
	})
	if err != nil {
		fmt.Println("CLIENT_ERR", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	vecs, err := client.Embed(ctx, []string{"验证嵌入接口是否可用"})
	if err != nil {
		fmt.Println("EMBED_FAIL", err)
		os.Exit(1)
	}
	fmt.Printf("EMBED_OK dims=%d\n", len(vecs[0]))
}
GOEOF
go run ./cmd/tmp-keycheck 2>&1 | tail -3
rm -rf cmd/tmp-keycheck
