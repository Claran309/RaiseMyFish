"""独立按rules.json解释评分；无模型/PG调用，不改历史文件。"""
import hashlib
import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).resolve().parent


def read(path):
    return json.loads(path.read_bytes())


def digest(data):
    return hashlib.sha256(data).hexdigest()


def contains(text, terms):
    return any(term in text for term in terms)


def matches(pattern, text):
    return re.search(pattern, text, flags=re.ASCII) is not None


def evaluate(c, output, book):
    text = output.strip()
    if not text:
        return ["输出为空"]
    scope = ("qq_private" if c.get("private") else "qq_group") if c["channel"] == "qq" else c["channel"]
    registry = {(r["side"], r["label"]): r for r in book["rules"]}
    failures = []
    for side in ("forbid", "expect"):
        for label in c[side]:
            rule = registry[side, label]
            required = not rule.get("any_when_input_contains") or contains(c["input"], rule["any_when_input_contains"])
            failed = bool(required and rule.get("any") and not contains(text, rule["any"])) or contains(text, rule.get("none", []))
            relation = rule.get("relationship")
            if relation:
                linked = (contains(text, rule.get("any", [])) or contains(c["input"], relation["input_any"])) and contains(text, relation["binding_any"])
                positive = text
                for phrase in relation["negated_phrases"]:
                    positive = positive.replace(phrase, "拒绝")
                if any(matches(pattern, positive) for pattern in relation["contradictions"]):
                    linked = False
                failed = not linked or contains(text, rule.get("none", []))
            failed |= bool(rule.get("max_runes", 0) and len(text) > rule["max_runes"])
            failed |= bool(rule.get("context_limit") and len(text) > book["limits"][scope])
            failed |= "max_newlines" in rule and text.count("\n") > rule["max_newlines"]
            failed |= bool(rule.get("count_token") and text.count(rule["count_token"]) > rule.get("max_count", 0))
            failed |= any(matches(pattern, text) for pattern in rule.get("patterns", []))
            if failed:
                failures.append(side + ":" + label)
            elif any(matches(pattern, text) for pattern in rule.get("review_patterns", [])):
                failures.append("review:" + label)
    return failures


def recompute(source, book, expected=None):
    counts = {"raw": 0, "delivered": 0}
    rows = []
    for index, row in enumerate(source["results"]):
        assert not row["redacted"]
        item = {"id": row["case"]["id"]}
        for side in counts:
            text = row[side + "_output"]
            assert digest(text.encode()) == row[side + "_sha256"]
            failures = evaluate(row["case"], text, book)
            if row.get("error"):
                failures.append("generation_error")
            passed = not failures
            counts[side] += passed
            item[side + "_pass"] = passed
            item[side + "_failures"] = failures
            if expected:
                scored = expected["results"][index]
                assert failures == (scored[side + "_failures"] or []), (item["id"], side, failures)
                assert passed == scored[side + "_pass"]
        rows.append(item)
    if expected:
        assert counts["raw"] == expected["raw_passed"] and counts["delivered"] == expected["delivered_passed"]
    return {**counts, "total": len(rows), "results": rows}


def main():
    inventory = read(HERE / "old-evidence-inventory.json")
    for row in inventory["files"] + inventory["original_persona"]:
        data = (ROOT / row["path"]).read_bytes()
        assert len(data) == row["bytes"] and digest(data) == row["sha256"], row["path"]
    first = "20261031T105248Z-501d0282"
    versions = {
        "v1": ROOT / "plans/reports/persona-eval" / first / "rules.json",
        "v2": ROOT / "plans/reports/persona-eval/20261031T121041Z-bcd11c0e/rules.json",
        "v3": ROOT / "plans/reports/persona-eval-rescore" / (first + "-v3-final") / "rules.json",
    }
    summary = []
    for run in (first, "20261031T121041Z-bcd11c0e", "20261031T124251Z-43a917e1"):
        source_dir = ROOT / "plans/reports/persona-eval" / run
        source = read(source_dir / "report.json")
        target_dir = ROOT / "plans/reports/persona-eval-rescore" / (run + "-v3-final")
        rescored = read(target_dir / "report.json")
        assert rescored["actual_model_calls"] == 0 and not rescored["new_model_validation"]
        assert rescored["source_report_sha256"] == digest((source_dir / "report.json").read_bytes())
        assert (target_dir / "rules.json").read_bytes() == versions["v3"].read_bytes()
        item = {"run_id": run}
        for name, path in versions.items():
            result = recompute(source, read(path), rescored if name == "v3" else None)
            item[name] = {k: result[k] for k in ("raw", "delivered", "total")}
        summary.append(item)
    result = {"kind": "independent_offline_rules_recompute", "actual_model_calls": 0, "old_evidence_unchanged": len(inventory["files"]), "rules_sha256": {k: digest(v.read_bytes()) for k, v in versions.items()}, "score_matrix": summary}
    (HERE / "offline-verification.json").write_bytes((json.dumps(result, ensure_ascii=False, indent=2) + "\n").encode())
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
