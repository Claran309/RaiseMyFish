"""侧写复核探针。默认fake/localhost；--postgres才运行独立schema测试。

不修改业务源码，不连接真实平台/LLM。失败/通过的意义见本轮审查报告。
"""
from pathlib import Path
import argparse
import json
import subprocess
import sys

sys.stdout.reconfigure(encoding="utf-8")
parser = argparse.ArgumentParser()
parser.add_argument("--postgres", action="store_true")
args = parser.parse_args()
here = Path(__file__).resolve().parent
root = here.parents[2]
scratch = (root / "tmp/review-20261110-evening-reproduce").resolve()
if root.resolve() not in scratch.parents:
    raise RuntimeError("临时目录必须属于当前仓库")
scratch.mkdir(parents=True, exist_ok=True)

def run(name, mappings, packages, pattern, integration=False):
    path = scratch / (name + ".json")
    path.write_text(json.dumps({"Replace": {str(root / k): str(here / v) for k, v in mappings.items()}}, indent=2), encoding="utf-8")
    command = ["go", "test", "-overlay", str(path)]
    if integration:
        command.append("-tags=integration")
    command += packages + ["-run", pattern, "-count=1", "-v"]
    result = subprocess.run(command, cwd=root, capture_output=True, text=True, encoding="utf-8", errors="replace")
    output = result.stdout + result.stderr
    (scratch / (name + ".txt")).write_text(output, encoding="utf-8")
    print(json.dumps({"probe": name, "exit_code": result.returncode, "output": output}, ensure_ascii=False))

run("summary-retry", {"internal/viewer/zz_review_summary_test.go": "summary_retry_probe.go.txt"}, ["./internal/viewer"], "^TestReviewSummary")
run("delivery", {
    "internal/sender/live/review_evening_test.go": "live_text_probe.go.txt",
    "internal/reasoning/review_evening_test.go": "viewer_tool_probe.go.txt",
    "internal/sender/dm/review_evening_test.go": "dm_unknown_probe.go.txt",
}, ["./internal/sender/live", "./internal/reasoning", "./internal/sender/dm"], "^TestReview")
if args.postgres:
    run("summary-source", {"internal/database/review_summary_source_test.go": "summary_source_probe.go.txt"}, ["./internal/database"], "^TestReviewSummarySource", True)
