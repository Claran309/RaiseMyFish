"""三通道收尾复核探针：fake/localhost，无真实平台或LLM。

默认不连接PG；--postgres才运行项目随机schema集成探针。
FAIL/PASS语义见收尾报告：部分探针断言正确不变量，部分断言缺陷行为。
物理Go探针为.go.txt，overlay映射虚拟测试，不修改业务源码。
"""
from pathlib import Path
import argparse
import json
import shutil
import subprocess
import sys

sys.stdout.reconfigure(encoding="utf-8")
parser = argparse.ArgumentParser()
parser.add_argument("--postgres", action="store_true")
args = parser.parse_args()
here = Path(__file__).resolve().parent
root = here.parents[2]
scratch = (root / "tmp/review-20261110-reproduction").resolve()
if root.resolve() not in scratch.parents:
    raise RuntimeError("临时目录必须位于本项目")
scratch.mkdir(parents=True, exist_ok=True)

def run(name, command):
    result = subprocess.run(command, cwd=root, capture_output=True, text=True, encoding="utf-8", errors="replace")
    output = result.stdout + result.stderr
    (scratch / (name + ".txt")).write_text(output, encoding="utf-8")
    print(json.dumps({"probe": name, "exit_code": result.returncode, "output": output}, ensure_ascii=False))

def overlay(name, entries):
    path = scratch / (name + ".json")
    path.write_text(json.dumps({"Replace": {str(root / target): str(here / source) for target, source in entries.items()}}, indent=2), encoding="utf-8")
    return str(path)

contextdir = scratch / "contextbudget"
contextdir.mkdir(exist_ok=True)
shutil.copy2(here / "context-budget.go.txt", contextdir / "main.go")
run("context-budget", ["go", "run", str(contextdir / "main.go")])
memory = overlay("memory", {
    "internal/brain/zz_review_memory_test.go": "correction_probe.go.txt",
    "internal/knowledge/zz_review_memory_test.go": "knowledge_probe.go.txt",
})
run("memory", ["go", "test", "-overlay", memory, "./internal/brain", "./internal/knowledge", "-run", "^TestReviewMemory", "-v", "-count=1"])
delivery = overlay("delivery", {
    "internal/sender/dm/review_20261109_test.go": "dm_unknown_review_test.go.txt",
    "internal/sender/durable/review_20261109_test.go": "durable_confirmation_review_test.go.txt",
})
run("delivery", ["go", "test", "-overlay", delivery, "./internal/sender/dm", "./internal/sender/durable", "-run", "^TestReview20261109", "-v", "-count=1"])
if args.postgres:
    pg = overlay("public-fact", {"internal/database/review_closeout_test.go": "publicfact_probe.go.txt"})
    run("public-fact", ["go", "test", "-overlay", pg, "-tags=integration", "./internal/database", "-run", "^TestCloseoutPublicFact", "-v", "-count=1"])
