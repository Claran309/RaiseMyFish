"""零模型/数据库调用，验证回滚、候选归档及全部旧证据。"""
import hashlib
import json
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).resolve().parent
ARCHIVE = ROOT / "plans/reports/scene-assembly-archive/20261031T124251Z-43a917e1"
BASE = ROOT / "plans/reports/persona-eval/20261031T121041Z-bcd11c0e"
EXPERIMENT = ROOT / "plans/reports/persona-eval/20261031T124251Z-43a917e1"


def read(path):
    return json.loads(path.read_bytes())


def digest(data):
    return hashlib.sha256(data).hexdigest()


def main():
    archived = read(ARCHIVE / "archive-manifest.json")["files"]
    by_original = {}
    for row in archived:
        path = (ARCHIVE / row["archive_path"]).resolve()
        assert path.is_relative_to(ARCHIVE.resolve())
        data = path.read_bytes()
        assert len(data) == row["bytes"] and digest(data) == row["sha256"], row["original_path"]
        by_original[row["original_path"]] = row
    inventory = read(HERE / "evidence-before-rollback.json")["files"]
    for row in inventory:
        data = (ROOT / row["path"]).read_bytes()
        assert len(data) == row["bytes"] and digest(data) == row["sha256"], row["path"]
    historical = read(ROOT / "plans/reports/phase-0.7c-scene-a-manifest.json")
    for row in historical["files"]:
        archived_row = by_original[row["path"]]
        assert row["sha256"] == archived_row["sha256"] and row["bytes"] == archived_row["bytes"]
    original_code = subprocess.check_output(["git", "show", "008beb6:internal/brain/context.go"], cwd=ROOT)
    current_code = (ROOT / "internal/brain/context.go").read_bytes()
    assert current_code == original_code
    candidate = read(EXPERIMENT / "report.json")
    for row in candidate["code_files"]:
        assert by_original[row["name"]]["sha256"] == row["sha256"]
    assert by_original["internal/brain/context.go"]["sha256"] != digest(current_code)
    for row in candidate["fixture_files"]:
        path = "personas/dafeiyu/eval/cases.jsonl" if row["name"] == "cases.jsonl" else "personas/dafeiyu/" + row["name"]
        assert digest((ROOT / path).read_bytes()) == row["sha256"] == by_original[path]["sha256"]
    # 使用被冻结的独立词法解释器，只调用recompute，不运行会写旧文件的main。
    interpreter = BASE / "verify.py"
    namespace = {"__name__": "frozen_rule_interpreter", "__file__": str(interpreter)}
    exec(compile(interpreter.read_bytes(), str(interpreter), "exec"), namespace)
    score = namespace["recompute"]
    baseline = read(BASE / "report.json")
    base_scores = score(baseline, BASE / "rules.json", baseline)
    candidate_scores = score(candidate, EXPERIMENT / "rules.json", candidate)
    assert base_scores["raw"] == 20 and candidate_scores["raw"] == 13
    assert candidate["provider_attempts"] == 35
    assert [json.loads(line) for line in (EXPERIMENT / "results.jsonl").read_bytes().splitlines()] == candidate["results"]
    assert (ROOT / "config/fishd.toml").read_bytes() == subprocess.check_output(["git", "show", "008beb6:config/fishd.toml"], cwd=ROOT)
    result = {
        "verification_kind": "offline_rollback_and_archive_audit",
        "actual_model_calls": 0,
        "database_calls": 0,
        "restored_code_commit": "008beb6",
        "restored_context_go_sha256": digest(current_code),
        "restored_context_go_byte_exact": True,
        "archived_files_verified": len(archived),
        "historical_delivery_files_recoverable": len(historical["files"]),
        "immutable_evidence_files_unchanged": len(inventory),
        "historical_v2_baseline": base_scores,
        "historical_rejected_experiment": candidate_scores,
        "historical_experiment_calls_not_new": 35,
        "new_model_run": False,
        "persona_cases_configuration_unchanged": True,
    }
    (HERE / "verification.json").write_bytes((json.dumps(result, ensure_ascii=False, indent=2) + "\n").encode())
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
