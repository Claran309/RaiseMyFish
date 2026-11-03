#!/usr/bin/env bash
# 跑 fishd 迁移（应用 0004_viewer_profiles.sql）
set -u
cd "$(dirname "$0")/.."
go run ./cmd/fishd --env-file .env migrate up 2>&1 | tail -5
