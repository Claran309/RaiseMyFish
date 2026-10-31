"""K0 离线迁移：只读快照 → 待审核清单 → 审核后发布；不调用模型或查询包装器。"""
import argparse
import collections
import hashlib
import json
import pathlib
import re
import sqlite3

VERSION = "fish-meme-import/v1"
SCENES = ("live", "comment", "dm", "qq_private", "qq_group")
# 首批仅选择普通陪伴可用的短资料；这些规则是候选筛选，不是人格评分器。
EXCLUDED = re.compile(
    r"主人|娇娇|大肥鱼|本鱼|魔鱼|NTR|绿帽|出轨|拆\s*CP|多角恋|"
    r"傻逼|傻比|煞笔|傻[卵狗吊叉]|弱智|智障|脑残|废物|司马|死妈|你妈|妈的|他妈|操你|去死|滚开|滚远|"
    r"色情|性交|强奸|炼铜|恋童|淫梦|野兽先辈|114514|鸡巴|牛子|政治梗|辱骂|人身攻击|"
    r"SESSDATA|bili_jct|api[_ -]?key|cookie|\.env|token|密码|配置文件|忽略.{0,8}指令",
    re.IGNORECASE,
)


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False).encode("utf-8")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write_identical(files):
    """先检查全部目标冲突；相同内容不改时间，不同内容绝不覆盖。"""
    for path, content in files.items():
        if path.exists() and (not path.is_file() or path.read_bytes() != content):
            raise ValueError("目标已有不同内容，拒绝覆盖")
    for path, content in files.items():
        if path.exists():
            continue
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("xb") as stream:
            stream.write(content)


def snapshot_source(source, archive_root):
    """同一个SQLite读取事务中导出两层全部字段；内容哈希才是快照身份。"""
    source, archive_root = pathlib.Path(source), pathlib.Path(archive_root)
    rows = []
    db = sqlite3.connect(source.resolve().as_uri() + "?mode=ro", uri=True)
    try:
        db.row_factory = sqlite3.Row
        db.execute("BEGIN")
        for table in ("memes", "custom"):
            for row in db.execute("SELECT * FROM " + table + " ORDER BY title"):
                rows.append({"layer": table, "row": dict(row)})
    finally:
        db.close()
    data = b"".join(canonical(row) + b"\n" for row in rows)
    sha = digest(data)
    manifest = {"schema": VERSION, "sha256": sha, "counts": dict(collections.Counter(row["layer"] for row in rows)), "rows": len(rows)}
    destination = archive_root / sha
    write_identical({destination / "rows.jsonl": data, destination / "manifest.json": canonical(manifest) + b"\n"})
    return destination


def load_snapshot(snapshot):
    snapshot = pathlib.Path(snapshot)
    metadata = json.loads((snapshot / "manifest.json").read_text(encoding="utf-8"))
    data = (snapshot / "rows.jsonl").read_bytes()
    if metadata.get("schema") != VERSION or metadata.get("sha256") != digest(data):
        raise ValueError("源快照哈希或格式无效")
    rows = [json.loads(line) for line in data.splitlines()]
    if any(row.get("layer") not in ("memes", "custom") or not isinstance(row.get("row"), dict) for row in rows):
        raise ValueError("源快照层或记录格式无效")
    if metadata.get("rows") != len(rows) or metadata.get("counts") != dict(collections.Counter(row["layer"] for row in rows)):
        raise ValueError("源快照数量不一致")
    return rows, metadata


def entry_id(row):
    title = row["row"].get("title")
    if not isinstance(title, str) or not title.strip():
        raise ValueError("源词条缺少原词")
    return "meme-" + row["layer"] + "-" + digest((row["layer"] + "\0" + title).encode("utf-8"))[:24]


def active_rows(rows):
    """同名custom覆盖内置，但源快照两层均保留。"""
    active = {}
    for layer in ("memes", "custom"):
        for row in rows:
            if row["layer"] == layer:
                entry_id(row)
                active[row["row"]["title"]] = row
    return [active[title] for title in sorted(active)]


def tags_for(row):
    raw = row["row"].get("tags")
    if raw is None:
        return []
    if not isinstance(raw, str):
        raise ValueError("源标签不是文本")
    return sorted(set(x.strip() for x in re.split(r"[、,，;；\s]+", raw) if x.strip()))


def exclusion_reason(row, max_usage=None):
    value = row["row"]
    usage = value.get("usage")
    if not isinstance(usage, str) or not usage.strip():
        return "missing_usage"
    if max_usage is not None and len(usage) > max_usage:
        return "long_usage"
    title = value.get("title")
    if not isinstance(title, str) or not title.strip():
        return "invalid_title"
    if EXCLUDED.search(title + "\n" + usage + "\n" + " ".join(tags_for(row))):
        return "first_batch_scope"
    if len((title + usage).encode("utf-8")) > 15000:
        return "entry_size"
    return None


def stratified(rows, count, seed):
    buckets = collections.defaultdict(list)
    for row in rows:
        tags = tags_for(row)
        buckets[tags[0] if tags else "未分类"].append(row)
    for values in buckets.values():
        values.sort(key=lambda row: digest((seed + "\0" + entry_id(row)).encode("utf-8")))
    result, position = [], 0
    while len(result) < count:
        advanced = False
        for key in sorted(buckets):
            if position < len(buckets[key]):
                result.append(buckets[key][position])
                advanced = True
                if len(result) == count:
                    break
        if not advanced:
            break
        position += 1
    return result


def propose(snapshot, count=500, seed="k0-first-500-v1", max_usage=140):
    rows, metadata = load_snapshot(snapshot)
    eligible = [row for row in active_rows(rows) if exclusion_reason(row, max_usage) is None]
    customs = [row for row in eligible if row["layer"] == "custom"]
    builtins = [row for row in eligible if row["layer"] == "memes"]
    custom_count = min(len(customs), max(1, count // 8))
    selected = stratified(customs, custom_count, seed) + stratified(builtins, count - custom_count, seed)
    if len(selected) < count:
        used = {entry_id(row) for row in selected}
        selected += stratified([row for row in eligible if entry_id(row) not in used], count - len(selected), seed)
    if len(selected) != count:
        raise ValueError("合格候选不足，不能编造或重复凑数")
    return {"schema": "fish-meme-selection/v1", "snapshot_sha256": metadata["sha256"], "seed": seed, "reviewed_by": "", "review_scope": "仅审核原词与原始接法；原始summary/origin等完整留归档，不注入百科出处", "entries": [{"id": entry_id(row), "approved": False, "channel": list(SCENES), "priority": 50} for row in selected]}


def render(entries):
    lines = ["---", "schema: fish-knowledge/v1", "entries:"]
    for entry in entries:
        for i, (key, value) in enumerate(entry.items()):
            lines.append(("  - " if i == 0 else "    ") + key + ": " + json.dumps(value, ensure_ascii=False))
    lines.append("---")
    return ("\n".join(lines) + "\n").encode("utf-8")


def publish(snapshot, selection, output, report, expected_count=500):
    rows, metadata = load_snapshot(snapshot)
    allowed_root = {"schema", "snapshot_sha256", "seed", "reviewed_by", "review_scope", "entries"}
    if set(selection) != allowed_root or selection.get("schema") != "fish-meme-selection/v1" or selection.get("snapshot_sha256") != metadata["sha256"]:
        raise ValueError("审核清单格式或源快照不符")
    if not isinstance(selection.get("reviewed_by"), str) or not selection["reviewed_by"].strip() or not selection.get("review_scope"):
        raise ValueError("缺少明确审核人和审核范围")
    choices = selection.get("entries")
    if not isinstance(choices, list) or len(choices) != expected_count:
        raise ValueError("审核清单条数不符合本批次要求")
    active = active_rows(rows)
    lookup = {entry_id(row): row for row in active}
    seen, entries = set(), []
    for choice in choices:
        if not isinstance(choice, dict) or set(choice) != {"id", "approved", "channel", "priority"} or choice["approved"] is not True:
            raise ValueError("条目未审核或存在未允许的修改字段")
        key = choice["id"]
        if key in seen or key not in lookup:
            raise ValueError("审核清单ID重复或不在有效源层")
        seen.add(key)
        channel, priority = choice["channel"], choice["priority"]
        if not isinstance(channel, list) or not channel or any(x not in SCENES for x in channel) or len(set(channel)) != len(channel):
            raise ValueError("审核通道无效")
        if type(priority) is not int or not 0 <= priority <= 100:
            raise ValueError("审核优先级无效")
        row = lookup[key]
        if exclusion_reason(row) is not None:
            raise ValueError("条目不符合首批公开短知识范围")
        original = row["row"]
        entry = {"id": key, "title": original["title"], "channel": channel, "keywords": [], "tags": tags_for(row), "priority": priority, "visibility": "public", "source": "meme-sqlite:" + row["layer"] + ":" + key, "summary": "", "usage": original["usage"]}
        # 与Go convertEntry同口径：正文包装、ID/来源、场景和所有触发元数据均计入。
        text = "主题：" + entry["title"] + "\n接法：" + entry["usage"]
        fields = [text, entry["id"], entry["source"], *entry["channel"], *entry["keywords"], *entry["tags"]]
        if sum(len(value.encode("utf-8")) for value in fields) > 16 << 10:
            raise ValueError("完整知识条目超过16KiB，未发布任何文件")
        entries.append(entry)
    entries.sort(key=lambda row: row["id"])
    data = render(entries)
    if len(data) > 1 << 20:
        raise ValueError("本批文件超过1MiB，必须先拆批")
    missing = sorted(entry_id(row) for row in rows if exclusion_reason(row) == "missing_usage")
    active_ids = {entry_id(row) for row in active}
    shadowed = sorted(entry_id(row) for row in rows if entry_id(row) not in active_ids)
    report_data = {"schema": VERSION, "source_sha256": metadata["sha256"], "source_counts": metadata["counts"], "source_rows": len(rows), "unique_titles": len(active), "published_count": len(entries), "published_ids": [x["id"] for x in entries], "output_sha256": digest(data), "selection_sha256": digest(canonical(selection)), "importer_sha256": digest(pathlib.Path(__file__).read_bytes()), "missing_usage_count": len(missing), "missing_usage_ids": missing, "shadowed_count": len(shadowed), "shadowed_ids": shadowed, "reviewed_by": selection["reviewed_by"], "review_scope": selection["review_scope"], "mapping": "title/usage原文不变；tags仅拆分；summary/origin/other及全部源字段留在原始归档，不进入首批prompt资料"}
    output, report = pathlib.Path(output), pathlib.Path(report)
    write_identical({output: data, report / "manifest.json": canonical(report_data) + b"\n", report / "selection.json": canonical(selection) + b"\n"})
    return report_data


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    snap = commands.add_parser("snapshot")
    snap.add_argument("--source", type=pathlib.Path, required=True)
    snap.add_argument("--archive-root", type=pathlib.Path, required=True)
    propose_parser = commands.add_parser("propose")
    propose_parser.add_argument("--snapshot", type=pathlib.Path, required=True)
    propose_parser.add_argument("--output", type=pathlib.Path, required=True)
    propose_parser.add_argument("--seed", default="k0-first-500-v1")
    propose_parser.add_argument("--count", type=int, default=500)
    propose_parser.add_argument("--max-usage", type=int, default=140)
    publish_parser = commands.add_parser("publish")
    publish_parser.add_argument("--snapshot", type=pathlib.Path, required=True)
    publish_parser.add_argument("--selection", type=pathlib.Path, required=True)
    publish_parser.add_argument("--output", type=pathlib.Path, required=True)
    publish_parser.add_argument("--report", type=pathlib.Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "snapshot":
            print(snapshot_source(args.source, args.archive_root))
        elif args.command == "propose":
            if args.count < 1 or args.max_usage < 1:
                raise ValueError("候选数量和长度必须为正数")
            selection = propose(args.snapshot, args.count, args.seed, args.max_usage)
            write_identical({args.output: canonical(selection) + b"\n"})
            print("候选已生成；全部未审核，不能直接发布")
        else:
            selection = json.loads(args.selection.read_text(encoding="utf-8"))
            result = publish(args.snapshot, selection, args.output, args.report)
            print(json.dumps({"published_count": result["published_count"], "output_sha256": result["output_sha256"]}))
    except (ValueError, OSError, sqlite3.Error, KeyError, TypeError):
        parser.exit(1, "迁移失败：检查源快照、审核清单或目标冲突；未覆盖已有内容。\n")


if __name__ == "__main__":
    main()
