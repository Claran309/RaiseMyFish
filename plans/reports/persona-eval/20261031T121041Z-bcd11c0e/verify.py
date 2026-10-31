"""Codex 独立按规则快照重算，无模型/数据库调用，不替代 DSH 验收。"""
import hashlib
import json
import re
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parents[4]
RUN = Path(__file__).resolve().parent
OLD = RUN.parent / "20261031T105248Z-501d0282"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def read(path):
    return json.loads(path.read_bytes())


def contains(text, terms):
    return any(term in text for term in terms)


def evaluate(case, output, book):
    text = output.strip()
    if not text:
        return ["输出为空"]
    context = case["channel"]
    if context == "qq":
        context = "qq_private" if case.get("private") else "qq_group"
    registry = {(r["side"], r["label"]): r for r in book["rules"]}
    failures = []
    for side in ("forbid", "expect"):
        for label in case[side]:
            rule = registry[side, label]
            any_terms, none = rule.get("any", []), rule.get("none", [])
            failed = bool(any_terms and not contains(text, any_terms)) or contains(text, none)
            relation = rule.get("relationship")
            if relation:
                linked = (contains(text, any_terms) or contains(case["input"], relation["input_any"])) and contains(text, relation["binding_any"])
                positive = text
                for phrase in relation["negated_phrases"]:
                    positive = positive.replace(phrase, "拒绝")
                if any(re.search(pattern, positive) for pattern in relation["contradictions"]):
                    linked = False
                failed = not linked or contains(text, none)
            failed |= bool(rule.get("max_runes", 0) and len(text) > rule["max_runes"])
            failed |= bool(rule.get("context_limit") and len(text) > book["limits"][context])
            failed |= "max_newlines" in rule and text.count("\n") > rule["max_newlines"]
            failed |= bool(rule.get("count_token") and text.count(rule["count_token"]) > rule.get("max_count", 0))
            failed |= any(re.search(pattern, text) for pattern in rule.get("patterns", []))
            if failed:
                failures.append(side + ":" + label)
            elif any(re.search(pattern, text) for pattern in rule.get("review_patterns", [])):
                failures.append("review:" + label)
    return failures


def recompute(source, rules, expected):
    book = read(rules)
    count = {"raw": 0, "delivered": 0}
    tags = Counter()
    for original, scored in zip(source["results"], expected["results"], strict=True):
        assert original["case"]["id"] == scored.get("id", scored.get("case", {}).get("id"))
        assert not original["redacted"]
        for side in count:
            output = original[side + "_output"]
            assert digest(output.encode()) == original[side + "_sha256"]
            actual = evaluate(original["case"], output, book)
            if original.get("error"):
                actual.append("generation_error")
            assert actual == (scored[side + "_failures"] or []), (original["case"]["id"], side, actual)
            assert (not actual) == scored[side + "_pass"]
            count[side] += not actual
            if side == "raw":
                tags.update(actual)
    assert count["raw"] == expected["raw_passed"]
    assert count["delivered"] == expected["delivered_passed"]
    return {**count, "total": len(source["results"]), "rules_sha256": digest(rules.read_bytes()), "failure_tags": dict(tags)}


def main():
    new, old = read(RUN / "report.json"), read(OLD / "report.json")
    rescored_dir = ROOT / "plans/reports/persona-eval-rescore/20261031T105248Z-501d0282-v2-final"
    compared_dir = Path(str(RUN) + "-compare-persona-lexical-v1")
    assert new["complete"] and not new["fatal"] and not new["interrupted"]
    assert new["total"] == new["completed"] == 35
    assert new["source_kind"] == "model_provider" and not new["baseline_used"]
    assert new["fixture_files"] == old["fixture_files"]
    assert digest((RUN / "rules.json").read_bytes()) == new["rules_sha256"]
    assert digest((RUN / "comparison-rules.json").read_bytes()) == new["comparison_rules_sha256"]
    assert (RUN / "comparison-rules.json").read_bytes() == (OLD / "rules.json").read_bytes()
    assert (RUN / "rules.json").read_bytes() == (rescored_dir / "rules.json").read_bytes()
    for source_dir, result_dir in ((OLD, rescored_dir), (RUN, compared_dir)):
        rescored = read(result_dir / "report.json")
        assert rescored["source_report_sha256"] == digest((source_dir / "report.json").read_bytes())
        assert rescored["source_rules_sha256"] == digest((source_dir / "rules.json").read_bytes())
        assert rescored["actual_model_calls"] == 0 and not rescored["new_model_validation"]
        assert rescored["source_kind"] == "rescore_existing_model_outputs" and rescored["source_original_kind"] == "model_provider"
    for item in new["code_files"]:
        assert digest((ROOT / item["name"]).read_bytes()) == item["sha256"], item["name"]
    for item in new["fixture_files"]:
        path = RUN / "cases.jsonl" if item["name"] == "cases.jsonl" else ROOT / "personas/dafeiyu" / item["name"]
        data = path.read_bytes()
        assert len(data) == item["bytes"] and digest(data) == item["sha256"]
    assert [json.loads(line) for line in (RUN / "results.jsonl").read_bytes().splitlines()] == new["results"]
    cases = [json.loads(line) for line in (RUN / "cases.jsonl").read_bytes().splitlines()]
    for snapshot, row in zip(cases, new["results"], strict=True):
        assert {**snapshot, "private": snapshot.get("private", False)} == {**row["case"], "private": row["case"].get("private", False)}
    system = "\n\n".join((ROOT / "personas/dafeiyu" / name).read_text(encoding="utf-8-sig").strip() for name in ("soul.md", "rules.md", "samples.md"))
    attempts = [a for row in new["results"] for a in row["attempts"]]
    assert all(a["system_sha256"] == digest(system.encode()) for a in attempts)
    assert len(attempts) == new["provider_attempts"] == 35
    assert all(a["ledger_stored"] and not a.get("error_kind") for a in attempts)
    assert sum(a["in_tokens"] for a in attempts) == new["in_tokens"]
    assert sum(a["out_tokens"] for a in attempts) == new["out_tokens"]
    summary = {
        "checked_by": "Codex独立Python重算；非DSH验收",
        "old_v1": recompute(old, OLD / "rules.json", old),
        "old_v2_offline": recompute(old, rescored_dir / "rules.json", read(rescored_dir / "report.json")),
        "new_v1_same_outputs": recompute(new, compared_dir / "rules.json", read(compared_dir / "report.json")),
        "new_v2_primary": recompute(new, RUN / "rules.json", new),
        "served_models": sorted({a["served_model"] for a in attempts}),
        "generation_errors": sum(bool(r.get("error")) for r in new["results"]),
        "attempts": len(attempts), "ledger_acknowledgements": sum(a["ledger_stored"] for a in attempts),
        "in_tokens": new["in_tokens"], "out_tokens": new["out_tokens"], "cost_usd": new["cost_usd"],
        "full_system_bytes": len(system.encode()), "full_system_retained_cases": 35,
        "prompt_estimate_range": [min(a["prompt_estimate"] for a in attempts), max(a["prompt_estimate"] for a in attempts)],
        "code_fixture_checkpoint_output_hashes_match": True,
        "complete": new["complete"], "reached_90": new["reached_90"],
    }
    (RUN / "verification.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
