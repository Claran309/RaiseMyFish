"""只读重算K0-7首轮指标与保护hash；不运行Reader/模型/PG。"""
import hashlib
import json
from collections import Counter
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[3]
EVAL = ROOT / "plans/reports/k0-7/20261101-retrieval-eval/run"


def read(path):
    return json.loads(path.read_bytes())


def digest(data):
    return hashlib.sha256(data).hexdigest()


def revision(files):
    return digest(("fish-knowledge/v1\n" + "".join(name + "\t" + digest(data) + "\n" for name, data in sorted(files.items()))).encode())


def main():
    freeze = read(EVAL / "freeze.json")
    result = read(EVAL / "results.json")
    assert result["freeze"] == freeze
    cases_path = ROOT / "internal/knowledgeeval/testdata/cases.jsonl"
    cases_data = cases_path.read_bytes()
    assert digest(cases_data) == freeze["cases_sha256"] == "c1111d04da4bd27ff2843f5dbb9373f5238db65a12d68645c7e9761b3b1551a1"
    cases = [json.loads(line) for line in cases_data.splitlines()]
    assert len(cases) == len(result["report"]["cases"]) == 100
    assert Counter(c["kind"] for c in cases) == {"positive": 60, "negative": 20, "permission": 20}
    corpus = {}
    for f in freeze["corpus"]:
        data = (ROOT / "personas/dafeiyu/knowledge" / f["path"]).read_bytes()
        assert digest(data) == f["sha256"]
        corpus[f["path"]] = data
    assert revision(corpus) == freeze["production_revision"]
    for name, key in (("batch-synthetic-acl.md", "acl_fixture_sha256"), ("batch-synthetic-order.md", "order_fixture_sha256")):
        assert digest((cases_path.parent / name).read_bytes()) == freeze[key]
    for f in freeze["code"]:
        assert digest((ROOT / f["path"]).read_bytes()) == f["sha256"], f["path"]
    totals = Counter()
    forms = {}
    for case, row in zip(cases, result["report"]["cases"], strict=True):
        for key in ("id", "kind", "form", "labels", "scope", "expected_ids"):
            assert row[key] == case[key]
        actual = row["actual_ids"] or []
        assert len(actual) <= 3 and len(actual) == len(set(actual))
        relevant = len(set(actual) & set(case["expected_ids"]))
        assert relevant == row["relevant"]
        hit = case["kind"] == "positive" and relevant > 0
        negative = case["kind"] == "negative" and bool(actual)
        assert row["positive_hit"] == hit and row["negative_false"] == negative
        totals["returned"] += len(actual)
        totals["relevant"] += relevant
        totals["positive_hits"] += hit
        totals["negative_false"] += negative
        totals["forbidden_returns"] += row["forbidden_returns"]
        expected_revision = freeze["synthetic_revision"] if case["kind"] == "permission" else freeze["production_revision"]
        assert row["revision"] == expected_revision
        if case["kind"] == "permission":
            assert row["control_scope"] == case["control_scope"] != case["scope"]
            assert set(case["blocked_ids"]) <= set(row["control_ids"])
            assert not (set(case["blocked_ids"]) & set(actual))
            assert row["control_passed"]
            totals["controls_passed"] += 1
        if case["kind"] == "positive":
            form = forms.setdefault(case["form"], {"total": 0, "hits": 0})
            form["total"] += 1
            form["hits"] += hit
        audit = row["budget"]
        assert audit["budget_percent"] == 10 and 0 <= audit["bytes"] <= audit["cap_bytes"] <= 32768 * 10 // 100
        assert audit["prompt_estimate"] <= 32768 and audit["prompt_estimate"] - audit["base_prompt_estimate"] == audit["bytes"]
        assert (audit["candidate_ids"] or []) == actual
        included, dropped = audit["included_ids"] or [], audit["dropped_ids"] or []
        assert set(included).isdisjoint(dropped) and set(included) | set(dropped) == set(actual)
        assert audit["fake_provider_calls"] == audit["fake_usage_rows"] == 2 and audit["passed"]
        totals["budget_passed"] += 1
    metrics = result["report"]["metrics"]
    for key, value in totals.items():
        assert metrics[key] == value, key
    assert totals == {"returned": 56, "relevant": 46, "positive_hits": 43, "negative_false": 0, "forbidden_returns": 0, "controls_passed": 20, "budget_passed": 100}
    assert metrics["precision"] == 46 / 56 and not result["report"]["quality_passed"] and not result["passed"]
    assert result["top3_order"]["passed"] and result["top3_order"]["repeats"] == 10
    assert result["top3_order"]["actual_ids"] == result["top3_order"]["expected_ids"] == ["sort-title", "sort-a", "sort-b"]
    for filename, enabled in (("check-default.json", False), ("check-enabled.json", True)):
        diagnostic = read(HERE / filename)
        assert diagnostic["database"] == "ready" and not diagnostic["provider_called"] and not diagnostic["usage_written"]
        assert diagnostic["knowledge"]["enabled"] == diagnostic["knowledge"]["assembled"] == enabled
        assert not diagnostic["knowledge"]["pipeline_connected"] and not diagnostic["business_pipeline_connected"]
        if enabled:
            assert diagnostic["knowledge"]["entries"] == 500 and diagnostic["knowledge"]["revision"] == freeze["production_revision"]
    inventory = read(HERE / "preservation.json")
    for f in inventory["protected"]:
        data = (ROOT / f["path"]).read_bytes()
        assert len(data) == f["bytes"] and digest(data) == f["sha256"], f["path"]
    verification = {"verification_kind": "independent_existing_results_and_diagnostic_audit", "reader_evaluation_reexecuted": False, "real_model_calls": 0, "production_pipeline_connected": False, "precision": 46 / 56, "relevant": 46, "returned": 56, "positive_hits": 43, "required_positive_hits": 48, "quality_passed": False, "negative_false": 0, "forbidden_returns": 0, "controls_passed": 20, "budget_passed": 100, "positive_forms": forms, "top3_order_passed": True, "production_revision": freeze["production_revision"], "frozen_code_files_verified": len(freeze["code"]), "protected_files_unchanged": len(inventory["protected"]), "diagnostic_model_and_usage_calls": 0}
    (HERE / "verification.json").write_bytes((json.dumps(verification, ensure_ascii=False, indent=2) + "\n").encode())
    print(json.dumps(verification, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
