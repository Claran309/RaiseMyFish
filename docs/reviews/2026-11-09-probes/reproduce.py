"""离线复核探针；不会连接真实平台、PG或LLM，不修改业务源码。

上下文和memory探针断言现有行为；delivery探针断言正确不变量，在受审基线预期失败。
修复后需重新解读结果，不能把旧输出视为新版本结论。
"""
from pathlib import Path
import json
import shutil
import subprocess
import sys

sys.stdout.reconfigure(encoding="utf-8")

here = Path(__file__).resolve().parent
root = here.parents[2]
scratch = (root / "tmp" / "review-20261109-reproduction").resolve()
if root.resolve() not in scratch.parents:
    raise RuntimeError("临时目录必须位于项目内")
scratch.mkdir(parents=True, exist_ok=True)
contextdir = scratch / "contextprobe"
contextdir.mkdir(exist_ok=True)
shutil.copy2(here / "context-probe.go.txt", contextdir / "main.go")

def run(name, args):
    result = subprocess.run(args, cwd=root, capture_output=True, text=True, encoding="utf-8", errors="replace")
    output = result.stdout + result.stderr
    (scratch / (name + ".txt")).write_text(output, encoding="utf-8")
    print(json.dumps({"probe": name, "exit_code": result.returncode, "output": output}, ensure_ascii=False))
    return result.returncode

def overlay(name, mappings):
    path = scratch / (name + ".json")
    data = {"Replace": {str(root / target): str(here / source) for target, source in mappings.items()}}
    path.write_text(json.dumps(data, indent=2), encoding="utf-8")
    return str(path)

run("context", ["go", "run", str(contextdir / "main.go")])
run("live-page", ["node", str(here / "live-page-probe.cjs")])
delivery = overlay("delivery-overlay", {
    "internal/sender/durable/review_20261109_test.go": "durable_review_test.go.txt",
    "internal/sender/dm/review_20261109_test.go": "dm_review_test.go.txt",
    "internal/collector/dm/review_20261109_test.go": "collector_dm_review_test.go.txt",
})
run("delivery", ["go", "test", "-overlay", delivery, "./internal/sender/durable", "./internal/sender/dm", "./internal/collector/dm", "-run", "^TestReview20261109", "-count=1", "-v"])
mem = overlay("memory-overlay", {
    "internal/memory/extractworker/zz_review_probe_test.go": "memory_soul_probe.go.txt",
    "internal/viewer/zz_review_probe_test.go": "memory_uid_probe.go.txt",
})
run("memory", ["go", "test", "-overlay", mem, "./internal/memory/extractworker", "./internal/viewer", "-run", "^TestReviewProbe", "-count=1", "-v"])
