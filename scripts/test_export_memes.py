"""迁移只读性、原词接法保留和幂等行为测试；仅使用临时源库。"""
import importlib.util
import json
import pathlib
import sqlite3
import tempfile
import unittest

MODULE_PATH = pathlib.Path(__file__).with_name("export_memes.py")
spec = importlib.util.spec_from_file_location("export_memes", MODULE_PATH)
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)


class MigrationTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)
        self.source = self.root / "source.db"
        db = sqlite3.connect(self.source)
        db.execute("create table memes(title text primary key,usage text,summary text,tags text,origin text,other text,slug text)")
        db.execute("create table custom(title text primary key,usage text,summary text,tags text,source text,updated_at text)")
        db.execute("insert into memes values (?,?,?,?,?,?,?)", ("同名", "内置接法", "原解释", "日常、吐槽", "原始出处", "其他数据", "slug"))
        db.execute("insert into memes values (?,?,?,?,?,?,?)", ("空接法", "", None, "", None, "", "empty"))
        db.execute("insert into memes values (?,?,?,?,?,?,?)", ("新词", '第一行\n  第二行"引号"', "原始说明", "日常", "出处", "保留", "new"))
        db.execute("insert into custom values (?,?,?,?,?,?)", ("同名", "自定义接法", "自定义解释", "日常,吐槽", "local", "2026-01-01"))
        db.commit()
        db.close()

    def snapshot(self):
        return migration.snapshot_source(self.source, self.root / "archive")

    def selection(self, snapshot):
        rows, metadata = migration.load_snapshot(snapshot)
        selected = [row for row in migration.active_rows(rows) if row["row"]["usage"]]
        return {"schema": "fish-meme-selection/v1", "snapshot_sha256": metadata["sha256"], "seed": "test", "reviewed_by": "测试审核", "review_scope": "原词与接法", "entries": [{"id": migration.entry_id(row), "approved": True, "channel": ["live", "comment", "dm", "qq_private", "qq_group"], "priority": 50} for row in selected]}

    def test_readonly_snapshot_preserves_fields_and_repeats(self):
        before = self.source.read_bytes()
        snapshot = self.snapshot()
        self.assertEqual(self.source.read_bytes(), before)
        self.assertEqual(self.snapshot(), snapshot)
        rows, meta = migration.load_snapshot(snapshot)
        self.assertEqual(len(rows), 4)
        self.assertEqual(meta["counts"], {"memes": 3, "custom": 1})
        self.assertEqual(next(r for r in rows if r["row"]["title"] == "新词")["row"]["other"], "保留")
        self.assertEqual(next(r for r in rows if r["row"]["title"] == "空接法")["row"]["summary"], None)
        active = migration.active_rows(rows)
        self.assertEqual(len(active), 3)
        self.assertEqual(next(r for r in active if r["row"]["title"] == "同名")["layer"], "custom")

    def test_publish_is_lossless_and_idempotent(self):
        snapshot = self.snapshot()
        selection = self.selection(snapshot)
        output, report = self.root / "memes.md", self.root / "report"
        result = migration.publish(snapshot, selection, output, report, expected_count=2)
        before = output.read_bytes()
        self.assertEqual(migration.publish(snapshot, selection, output, report, expected_count=2), result)
        self.assertEqual(output.read_bytes(), before)
        text = output.read_text(encoding="utf-8")
        self.assertIn("自定义接法", text)
        self.assertNotIn("内置接法", text)
        self.assertIn('第一行\\n  第二行\\"引号\\"', text)
        self.assertEqual(result["published_count"], 2)
        self.assertEqual(result["missing_usage_count"], 1)
        self.assertEqual(result["shadowed_count"], 1)
        self.assertNotIn(str(self.source), json.dumps(result, ensure_ascii=False))
        output.write_text("人工改动", encoding="utf-8")
        with self.assertRaises(ValueError):
            migration.publish(snapshot, selection, output, report, expected_count=2)
        self.assertEqual(output.read_text(encoding="utf-8"), "人工改动")

    def test_publish_rejects_unreviewed_wrong_snapshot_duplicates_and_private(self):
        snapshot = self.snapshot()
        base = self.selection(snapshot)
        cases = []
        for action in (lambda s: s.update(snapshot_sha256="0" * 64), lambda s: s["entries"][0].update(approved=False), lambda s: s["entries"].append(s["entries"][0]), lambda s: s["entries"][0].update(channel=["invalid"]), lambda s: s["entries"][0].update(visibility="private")):
            selection = json.loads(json.dumps(base))
            action(selection)
            cases.append(selection)
        for i, selection in enumerate(cases):
            with self.subTest(i=i), self.assertRaises(ValueError):
                migration.publish(snapshot, selection, self.root / f"bad-{i}.md", self.root / f"bad-{i}", expected_count=2)

    def test_snapshot_and_report_conflicts_are_not_overwritten(self):
        snapshot = self.snapshot()
        (snapshot / "rows.jsonl").write_text("人工改动", encoding="utf-8")
        with self.assertRaises(ValueError):
            self.snapshot()
        with self.assertRaises(ValueError):
            migration.load_snapshot(snapshot)

    def test_publish_checks_entire_entry_size_before_writing(self):
        db = sqlite3.connect(self.source)
        try:
            db.execute("update memes set tags=? where title=?", ("x" * 20000, "新词"))
            db.commit()
        finally:
            db.close()
        snapshot = self.snapshot()
        selection = self.selection(snapshot)
        output, report = self.root / "large.md", self.root / "large-report"
        with self.assertRaises(ValueError):
            migration.publish(snapshot, selection, output, report, expected_count=2)
        self.assertFalse(output.exists())
        self.assertFalse(report.exists())

    def test_candidate_selection_is_stable_and_requires_review(self):
        snapshot = self.snapshot()
        one = migration.propose(snapshot, count=2, seed="fixed", max_usage=140)
        self.assertEqual(one, migration.propose(snapshot, count=2, seed="fixed", max_usage=140))
        self.assertEqual(len(one["entries"]), 2)
        self.assertTrue(all(not x["approved"] for x in one["entries"]))
        with self.assertRaises(ValueError):
            migration.publish(snapshot, one, self.root / "unreviewed.md", self.root / "unreviewed", expected_count=2)


if __name__ == "__main__":
    unittest.main()
