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
        try:
            import numpy as np
        except ImportError:
            self.skipTest("numpy unavailable")

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

    def test_calibration_classifies_missing_reference_and_near_miss(self):
        row = {
            "truth_person_id": 2,
            "represented": True,
            "top_person_id": 1,
            "top_similarity": 0.61,
            "second_similarity": 0.60,
            "margin": 0.01,
            "true_similarity": 0.59,
            "quality": 0.9,
            "person_scores": [{"person_id": 1, "support_count": 2}],
        }
        result = review_tool.classify_calibration_row(row, {"similarity_threshold": .45, "margin_threshold": .05, "min_support": 1, "quality_threshold": .25}, {2}, .03)
        self.assertEqual(result["comparison"], "incorrect_prediction")
        self.assertEqual(result["decision"], "review")
        self.assertTrue(result["near_miss"])

        row["truth_person_id"] = 3
        result = review_tool.classify_calibration_row(row, {"similarity_threshold": .45, "margin_threshold": .05, "min_support": 1, "quality_threshold": .25}, {2}, .03)
        self.assertEqual(result["comparison"], "known_person_without_reference")

    def test_mutual_knn_groups_and_metrics(self):
        try:
            import numpy as np
        except ImportError:
            self.skipTest("numpy unavailable")

        face_ids = [10, 20, 30, 40]
        embeddings = np.array([[1., 0.], [.99, .1], [0., 1.], [0., .99]], dtype="<f4")
        embeddings /= np.linalg.norm(embeddings, axis=1, keepdims=True)
        neighbors, comparisons = review_tool.exact_top_neighbors(face_ids, embeddings, 1)
        self.assertEqual(comparisons, 12)
        clusters, assignments, edges = review_tool.mutual_knn_groups(face_ids, neighbors, 1, .99)
        self.assertEqual(edges, 2)
        self.assertEqual({frozenset(face_ids[index] for index in cluster["indices"]) for cluster in clusters}, {frozenset({10, 20}), frozenset({30, 40})})
        rows = [{"face_id": face_id, "kind": "person", "person_id": person_id} for face_id, person_id in zip(face_ids, (1, 1, 2, 2))]
        metrics = review_tool.grouping_metrics(rows, assignments, clusters, {1}, "all")
        self.assertEqual(metrics["purity"], 1.0)
        self.assertEqual(metrics["pair_precision"], 1.0)
        self.assertEqual(metrics["pair_recall"], 1.0)
        self.assertEqual(metrics["mixed_person_clusters"], 0)

    def test_grouping_population_excludes_unrepresented_people(self):
        rows = [
            {"face_id": 1, "kind": "person", "person_id": 10},
            {"face_id": 2, "kind": "person", "person_id": 20},
            {"face_id": 3, "kind": "unknown", "person_id": None},
        ]
        assignments = {1: 1, 2: 2, 3: 3}
        clusters = [{"cluster_id": index, "indices": [index - 1]} for index in range(1, 4)]
        reference = review_tool.grouping_population_rows(rows, {10}, "reference")
        non_reference = review_tool.grouping_population_rows(rows, {10}, "non_reference")
        self.assertEqual([row["face_id"] for row in reference], [1])
        self.assertEqual([row["face_id"] for row in non_reference], [2, 3])


if __name__ == "__main__":
    unittest.main()
