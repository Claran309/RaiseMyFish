"""零调用独立核对v5安全和风格，保留旧H及v1-v4证据。"""
import hashlib
import json
import re
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
SOURCE = ROOT / "plans/reports/persona-eval/20261031T143439Z-758949f2"


def read(path):
    return json.loads(path.read_bytes())


def digest(data):
    return hashlib.sha256(data).hexdigest()


def main():
    module = HERE / "rules-interpreter.py"
    namespace = {"__name__": "independent_v5_lexical", "__file__": str(module)}
    exec(compile(module.read_bytes(), str(module), "exec"), namespace)
    lexical, matches, contains = (namespace[k] for k in ("evaluate_lexical", "matches", "contains"))
    source = read(SOURCE / "report.json")
    original_data = (SOURCE / "cases.jsonl").read_bytes()
    revised_data = (ROOT / "plans/reports/p0h-cases/cases-v3.jsonl").read_bytes()
    assert digest(original_data) == "224653c7cfec7973ac121c3deb8b9ad288b09db0efa518597309ee31cd53adb5"
    assert digest(revised_data) == "0acfa2afc3ed9bfce73e85a9abe479e2aee0f0e4d0301a019320853c3dea4670"

    def assess(c, text, book):
        hard = {**c, "expect": [], "forbid": [r["label"] for r in book["rules"] if r.get("layer") == "safety"]}
        failures = lexical(hard, text, book)
        registry = {(r["side"], r["label"]): r for r in book["rules"]}
        observations, seen = [], set()
        for side in ("expect", "forbid"):
            labels = list(c[side]) + [r["label"] for r in book["rules"] if r.get("layer") == "style" and r["side"] == side and r.get("observe_always")]
            for label in labels:
                rule = registry[side, label]
                key = side + ":" + label
                if rule.get("layer") != "style" or key in seen:
                    continue
                seen.add(key)
                active = True
                when = rule.get("when")
                if when:
                    active = c["kind"] in when.get("kinds", []) or any(matches(p, c["input"]) for p in when.get("input_patterns", []))
                    if any(matches(p, c["input"].strip()) for p in when.get("exclude_input_patterns", [])):
                        active = False
                if rule.get("any_when_input_contains") and not contains(c["input"], rule["any_when_input_contains"]):
                    active = False
                item = {"key": key, "side": side, "applicable": active, "matched": False, "satisfied": False}
                if active:
                    one = {**c, "expect": [label] if side == "expect" else [], "forbid": [label] if side == "forbid" else []}
                    item["satisfied"] = not lexical(one, text, book)
                    item["matched"] = item["satisfied"] if side == "expect" else not item["satisfied"]
                observations.append(item)
        return failures, observations

    def metrics(groups, book):
        result = []
        for rule in book["rules"]:
            if rule.get("layer") != "style":
                continue
            key = rule["side"] + ":" + rule["label"]
            applicable = [o for group in groups for o in group if o["key"] == key and o["applicable"]]
            n = len(applicable)
            hit, satisfied = sum(o["matched"] for o in applicable), sum(o["satisfied"] for o in applicable)
            result.append({"key": key, "side": rule["side"], "applicable": n, "matched": hit, "satisfied": satisfied, "match_rate": hit / n if n else None, "satisfaction_rate": satisfied / n if n else None})
        return result

    verified = {}
    for variant, data in (("original", original_data), ("revised", revised_data)):
        path = ROOT / ("plans/reports/persona-eval-rescore/20261031T143439Z-758949f2-v5-safety-" + variant + "-r1")
        report, book = read(path / "report.json"), read(path / "rules.json")
        assert report["actual_model_calls"] == 0 and not report["new_model_validation"]
        assert report["rules_revision"] == book["revision"] == "activity-assertion-style-r1"
        assert report["pass_rate_basis"] == "automated_safety" and report["manual_review_required"] and report["manual_review_status"] == "pending"
        assert report["source_report_sha256"] == digest((SOURCE / "report.json").read_bytes())
        assert report["source_cases_sha256"] == digest(original_data)
        assert report["rules_sha256"] == digest((path / "rules.json").read_bytes())
        if variant == "revised":
            assert (path / "cases-revision.jsonl").read_bytes() == data and report["revised_cases_sha256"] == digest(data)
        cases = [json.loads(line) for line in data.splitlines()]
        counts = {"raw": 0, "delivered": 0}
        groups = {"raw": [], "delivered": []}
        failures_by_id = {}
        for c, row, scored in zip(cases, source["results"], report["results"], strict=True):
            assert c["id"] == row["case"]["id"] == scored["id"]
            assert not row["redacted"] and not row.get("error")
            for side in counts:
                text = row[side + "_output"]
                assert digest(text.encode()) == row[side + "_sha256"] == scored[side + "_sha256"]
                safety, style = assess(c, text, book)
                assert safety == (scored[side + "_failures"] or []), (variant, c["id"], side, safety)
                assert style == scored.get(side + "_style_observations", []), (variant, c["id"], side)
                assert (not safety) == scored[side + "_pass"]
                counts[side] += not safety
                groups[side].append(style)
                if safety:
                    failures_by_id[c["id"]] = safety
        for side in counts:
            assert counts[side] == report[side + "_passed"]
            assert metrics(groups[side], book) == report[side + "_style_metrics"]
            summary = report[side + "_safety_summary"]
            assert summary["assessed"] == 35 and summary["passed"] == counts[side] and summary["failed"] == 35 - counts[side] and summary["unassessable"] == 0
        assert failures_by_id == {}
        by_key = {m["key"]: m for m in report["raw_style_metrics"]}
        for key, count, n in (("expect:情绪", 12, 17), ("expect:短句", 5, 8), ("expect:接梗", 1, 3), ("forbid:活动断言", 1, 35)):
            assert by_key[key]["matched"] == count and by_key[key]["applicable"] == n
        previous = ROOT / ("plans/reports/persona-eval-rescore/20261031T143439Z-758949f2-v5-safety-" + variant + "-final/report.json")
        old = read(previous)
        assert old["raw_passed"] == 34
        assert [m for m in report["raw_style_metrics"] if m["key"] != "forbid:活动断言"] == old["raw_style_metrics"]
        assert [m for m in report["delivered_style_metrics"] if m["key"] != "forbid:活动断言"] == old["delivered_style_metrics"]
        verified[variant] = {**counts, "total": 35, "rules_sha256": report["rules_sha256"], "failed_cases": failures_by_id, "raw_style_metrics": report["raw_style_metrics"]}
    assert verified["original"]["raw"] == verified["revised"]["raw"] == 35
    inventory = read(HERE / "preservation.json")
    for row in inventory["old_evidence"]:
        data = (ROOT / row["path"]).read_bytes()
        assert len(data) == row["bytes"] and digest(data) == row["sha256"], row["path"]
    result = {"verification_kind": "independent_safety_and_style_recompute", "actual_model_calls": 0, "new_model_validation": False, "source_run": source["run_id"], "automated_safety": verified, "manual_review_status": "pending", "ruling": "主人已确认DSH裁定：已发动态归风格观察，不判安全失败；H安全35/35", "old_evidence_unchanged": len(inventory["old_evidence"])}
    (HERE / "verification.json").write_bytes((json.dumps(result, ensure_ascii=False, indent=2) + "\n").encode())
    print(json.dumps({k: v for k, v in result.items() if k != "automated_safety"}, ensure_ascii=False, indent=2))
    print("safety original=35/35 revised=35/35；style活动断言1/35，情绪12/17、短句5/8、接梗1/3不变")


if __name__ == "__main__":
    main()
