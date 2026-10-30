import os
import pathlib
import signal
import subprocess
import tempfile
import time
import unittest


REPOSITORY = pathlib.Path(__file__).parents[1]
SETUP_SCRIPT = REPOSITORY / "scripts" / "setup_worker.sh"


class WorkerSetupTests(unittest.TestCase):
    def test_interrupted_install_cleans_temporary_environment(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            venv = root / "venv"
            model_root = root / "models"
            environment = os.environ.copy()
            environment.update(
                {
                    "FACE_ME_BRO_VENV": str(venv),
                    "FACE_ME_BRO_MODEL_ROOT": str(model_root),
                    "PYTHON_BIN": "/opt/homebrew/bin/python3.11",
                    "PIP_INDEX_URL": "http://127.0.0.1:9/simple",
                    "PIP_RETRIES": "0",
                    "PIP_DEFAULT_TIMEOUT": "1",
                }
            )
            process = subprocess.Popen(
                [str(SETUP_SCRIPT)],
                cwd=REPOSITORY,
                env=environment,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            temporary = None
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline and process.poll() is None:
                candidates = list(root.glob("venv.installing.*"))
                if candidates:
                    temporary = candidates[0]
                    break
                time.sleep(0.05)
            if temporary is None:
                output, error = process.communicate(timeout=5)
                self.fail(f"setup did not create temporary venv: {output}\n{error}")
            process.send_signal(signal.SIGTERM)
            process.communicate(timeout=10)
            self.assertFalse(temporary.exists(), f"temporary setup directory remains: {temporary}")
            self.assertFalse(venv.exists(), "interrupted setup published a partial venv")


if __name__ == "__main__":
    unittest.main()
