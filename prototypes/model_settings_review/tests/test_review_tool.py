import sqlite3
import tempfile
import unittest
from pathlib import Path

import sys

sys.path.insert(0, str(Path(__file__).parents[1] / "src"))
import review_tool


class ReviewToolTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.db = sqlite3.connect(":memory:")
        self.db.row_factory = sqlite3.Row
        review_tool.init_db(self.db)

    def tearDown(self):
        self.db.close()
        self.temp.cleanup()

    def test_schema_and_metadata(self):
        review_tool.set_meta(self.db, "example", {"ok": True})
        self.db.commit()
        self.assertEqual(review_tool.get_meta(self.db, "example"), {"ok": True})
        tables = {row["name"] for row in self.db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        self.assertIn("faces", tables)
        self.assertIn("predictions", tables)

    def test_cosine(self):
        class Vector:
            def __matmul__(self, other):
                return 1.0

            def __mul__(self, other):
                return 1.0

        # The real numeric path is exercised when numpy is installed; this
        # assertion also keeps the test independent of model downloads.
        try:
            import numpy as np
        except ImportError:
            self.skipTest("numpy unavailable")
        self.assertAlmostEqual(review_tool.cosine(np.array([1.0, 0.0]), np.array([1.0, 0.0])), 1.0)
        self.assertAlmostEqual(review_tool.cosine(np.array([1.0, 0.0]), np.array([0.0, 1.0])), 0.0)

    def test_prediction_and_blind_review_gate(self):
        import numpy as np

        self.db.execute("INSERT INTO assets(content_sha256,size,created_at) VALUES('asset',1,'now')")
        asset_id = self.db.execute("SELECT id FROM assets").fetchone()[0]
        self.db.execute("INSERT INTO images(asset_id,root_path,relative_path,absolute_path,path_sha256,status) VALUES(?,?,?,?,?,?)", (asset_id, '.', 'x.jpg', '', 'path', 'ok'))
        image_id = self.db.execute("SELECT id FROM images").fetchone()[0]
        vectors = [np.array([1., 0.], dtype='<f4'), np.array([0., 1.], dtype='<f4'), np.array([.99, .01], dtype='<f4')]
        for index, item in enumerate(vectors):
            self.db.execute("INSERT INTO faces(image_id,face_index,x1,y1,x2,y2,detection_confidence,quality,embedding,embedding_dimension) VALUES(?,?,?,?,?,?,?,?,?,?)", (image_id, index, 0, 0, 10, 10, .99, .9, item.tobytes(), 2))
        self.db.execute("INSERT INTO people(label,created_at) VALUES('Alice','now')")
        self.db.execute("INSERT INTO people(label,created_at) VALUES('Bob','now')")
        alice, bob = [r[0] for r in self.db.execute("SELECT id FROM people ORDER BY id")]
        face_ids = [r[0] for r in self.db.execute("SELECT id FROM faces ORDER BY id")]
        self.db.execute("INSERT INTO labels(face_id,role,kind,person_id,created_at) VALUES(?,?,?,?,?)", (face_ids[0], 'reference', 'person', alice, 'now'))
        self.db.execute("INSERT INTO labels(face_id,role,kind,person_id,created_at) VALUES(?,?,?,?,?)", (face_ids[1], 'reference', 'person', bob, 'now'))
        with self.assertRaises(ValueError):
            review_tool.generate_predictions(self.db, review_tool.default_settings())
        self.db.execute("INSERT INTO labels(face_id,role,kind,person_id,created_at) VALUES(?,?,?,?,?)", (face_ids[2], 'evaluation', 'person', alice, 'now'))
        self.db.commit()
        result = review_tool.generate_predictions(self.db, review_tool.default_settings())
        self.assertEqual(result["predictions"], 1)
        row = self.db.execute("SELECT person_id,category FROM predictions").fetchone()
        self.assertEqual(row["person_id"], alice)
        self.assertEqual(row["category"], "high_confidence")


if __name__ == "__main__":
    unittest.main()
