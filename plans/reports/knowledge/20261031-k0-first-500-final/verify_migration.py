"""独立核对首批原词/接法、来源层、哈希、完整条目限额和审核覆盖；不调用模型。"""
import collections
import hashlib
import json
import pathlib
import re


def sha(data):
    return hashlib.sha256(data).hexdigest()


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False).encode("utf-8")


def main():
    report = pathlib.Path(__file__).resolve().parent
    root = report.parents[3]
    manifest = json.loads((report / "manifest.json").read_text(encoding="utf-8"))
    selection = json.loads((report / "selection.json").read_text(encoding="utf-8"))
    reviews = json.loads((report / "review-decisions.json").read_text(encoding="utf-8"))
    assert sha(canonical(selection)) == manifest["selection_sha256"]
    assert sha((root / "scripts/export_memes.py").read_bytes()) == manifest["importer_sha256"]
    archive = root / "data/knowledge-import" / manifest["source_sha256"]
    raw = (archive / "rows.jsonl").read_bytes()
    assert sha(raw) == manifest["source_sha256"]
    rows = [json.loads(line) for line in raw.splitlines()]
    assert len(rows) == manifest["source_rows"]
    assert dict(collections.Counter(x["layer"] for x in rows)) == manifest["source_counts"]
    active = {}
    for layer in ("memes", "custom"):
        for row in rows:
            if row["layer"] == layer:
                active[row["row"]["title"]] = row
    assert len(active) == manifest["unique_titles"]
    by_id = {}
    for row in active.values():
        key = "meme-" + row["layer"] + "-" + sha((row["layer"] + "\0" + row["row"]["title"]).encode("utf-8"))[:24]
        by_id[key] = row
    data = (root / "personas/dafeiyu/knowledge/memes.md").read_bytes()
    assert sha(data) == manifest["output_sha256"]
    lines = data.decode("utf-8").splitlines()
    assert lines[:3] == ["---", "schema: fish-knowledge/v1", "entries:"] and lines[-1] == "---"
    entries = []
    for line in lines[3:-1]:
        matched = re.fullmatch(r"(  - |    )([a-z_]+): (.*)", line)
        assert matched, "发布文件不是已定义的确定序列化格式"
        if matched[1] == "  - ":
            entries.append({})
        assert entries and matched[2] not in entries[-1]
        entries[-1][matched[2]] = json.loads(matched[3])
    assert len(entries) == len(selection["entries"]) == manifest["published_count"] == 500
    assert [x["id"] for x in entries] == manifest["published_ids"]
    assert {x["id"] for x in entries} == {x["id"] for x in selection["entries"]}
    assert all(x["approved"] is True for x in selection["entries"])
    rejected = set(reviews["base_rejected"]) | set(reviews["replacement_rejected"])
    assert not rejected.intersection(manifest["published_ids"])
    assert set(manifest["published_ids"]).issubset(reviews["reviewed_ids"])
    max_bytes = 0
    for entry in entries:
        source = by_id[entry["id"]]
        original = source["row"]
        assert entry["title"] == original["title"] and entry["usage"] == original["usage"]
        assert entry["summary"] == "" and entry["visibility"] == "public"
        assert entry["source"] == "meme-sqlite:" + source["layer"] + ":" + entry["id"]
        assert entry["tags"] == sorted(set(x.strip() for x in re.split(r"[、,，;；\s]+", original.get("tags") or "") if x.strip()))
        text = "主题：" + entry["title"] + "\n接法：" + entry["usage"]
        size = sum(len(value.encode("utf-8")) for value in [text, entry["id"], entry["source"], *entry["channel"], *entry["keywords"], *entry["tags"]])
        assert size <= 16384
        max_bytes = max(max_bytes, size)
    result = {"schema": "fish-meme-verification/v1", "actual_model_calls": 0, "raw_rows": len(rows), "unique_titles": len(active), "reviewed_count": len(reviews["reviewed_ids"]), "rejected_count": len(rejected), "published_count": len(entries), "original_title_usage_equal": True, "hashes_match": True, "max_entry_payload_bytes": max_bytes, "relevance_100_query_eval": "deferred_K0-7", "brain_injection": "deferred_K0-5"}
    content = json.dumps(result, ensure_ascii=False, indent=2) + "\n"
    target = report / "verification.json"
    if target.exists():
        assert target.read_text(encoding="utf-8") == content, "已有独立核对结果不同，拒绝覆盖"
    else:
        with target.open("x", encoding="utf-8", newline="\n") as stream:
            stream.write(content)
    print(json.dumps(result, ensure_ascii=True))


if __name__ == "__main__":
    main()
