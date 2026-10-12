import importlib.util
import json
import pathlib
import tempfile
import unittest
from contextlib import redirect_stdout
from io import StringIO
from unittest.mock import patch


MODULE_PATH = pathlib.Path(__file__).with_name("setup_models.py")
SPEC = importlib.util.spec_from_file_location("setup_models", MODULE_PATH)
setup_models = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(setup_models)


class SetupModelTests(unittest.TestCase):
    def test_offline_setup_creates_and_then_verifies_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            model_dir = pathlib.Path(directory) / "models" / "buffalo_l"
            model_dir.mkdir(parents=True)
            (model_dir / "model.onnx").write_bytes(b"model")
            output = StringIO()
            with patch("sys.argv", ["setup_models.py", "--root", directory, "--offline"]), redirect_stdout(output):
                setup_models.main()
            result = json.loads(output.getvalue())
            self.assertTrue(result["offline"])
            manifest = model_dir / "checksums.json"
            self.assertTrue(manifest.exists())
            with patch("sys.argv", ["setup_models.py", "--root", directory, "--offline"]):
                setup_models.main()

    def test_offline_setup_rejects_changed_model(self):
        with tempfile.TemporaryDirectory() as directory:
            model_dir = pathlib.Path(directory) / "models" / "buffalo_l"
            model_dir.mkdir(parents=True)
            model_path = model_dir / "model.onnx"
            model_path.write_bytes(b"model")
            with patch("sys.argv", ["setup_models.py", "--root", directory, "--offline"]):
                setup_models.main()
            model_path.write_bytes(b"changed")
            with patch("sys.argv", ["setup_models.py", "--root", directory, "--offline"]):
                with self.assertRaises(SystemExit):
                    setup_models.main()

    def test_host_setup_and_docker_contracts_are_pinned(self):
        repository = MODULE_PATH.parents[1]
        setup_script = (repository / "scripts" / "setup_worker.sh").read_text()
        dockerfile = (repository / "docker" / "Dockerfile").read_text()
        self.assertIn(".installing.$$", setup_script)
        self.assertIn("trap cleanup EXIT", setup_script)
        self.assertIn("--offline", setup_script)
        self.assertIn("requirements.lock", setup_script)
        self.assertIn("COPY worker/requirements.lock", dockerfile)
        self.assertIn("setup_models.py --model buffalo_l", dockerfile)
        self.assertIn('ENTRYPOINT ["python", "worker/insightface_worker.py"]', dockerfile)


if __name__ == "__main__":
    unittest.main()
