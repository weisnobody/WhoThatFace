import importlib.util
import hashlib
import io
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import types
import unittest
from contextlib import redirect_stdout


MODULE_PATH = pathlib.Path(__file__).with_name("insightface_worker.py")
SPEC = importlib.util.spec_from_file_location("insightface_worker", MODULE_PATH)
worker = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(worker)


class Face:
    def __init__(self, left, top):
        self.bbox = [left, top, left + 10, top + 10]


class WorkerContractTests(unittest.TestCase):
    def test_capabilities_include_pipeline_and_resource_contract(self):
        worker.RUNTIME.update(
            pipeline_id="pipeline-test",
            modules=["detection", "recognition"],
            max_pixels=100,
            max_width=20,
            max_height=30,
        )
        capabilities = worker.capabilities()
        self.assertEqual(capabilities["pipeline_id"], "pipeline-test")
        self.assertEqual(capabilities["supported_commands"], ["hello", "analyze"])
        self.assertEqual(capabilities["max_width"], 20)
        self.assertEqual(capabilities["max_height"], 30)

    def test_detections_are_stable(self):
        ordered = worker.ordered_detections([Face(20, 5), Face(1, 5), Face(1, 2)])
        self.assertEqual([(face.bbox[0], face.bbox[1]) for face in ordered], [(1, 2), (1, 5), (20, 5)])

    def test_hello_negotiates_protocol_and_reports_capabilities(self):
        output = io.StringIO()
        with redirect_stdout(output):
            self.assertTrue(worker.handle({"command": "hello", "id": "hello-1", "protocol_version": 1}))
        response = json.loads(output.getvalue())
        self.assertEqual(response["command"], "hello_ack")
        self.assertEqual(response["protocol_major"], 1)
        self.assertIn("analyze", response["capabilities"]["supported_commands"])

    def test_analyze_enforces_width_limit_before_model_call(self):
        class Image:
            shape = (40, 20, 3)

        class Model:
            def get(self, _image):
                raise AssertionError("model should not run past resource validation")

        with tempfile.NamedTemporaryFile() as source:
            source.write(b"image")
            source.flush()
            old_model = worker.MODEL
            old_loader = worker.load_oriented_image
            old_width = worker.RUNTIME["max_width"]
            old_cv2 = sys.modules.get("cv2")
            worker.MODEL = Model()
            worker.load_oriented_image = lambda _path, _cv2, *_limits: Image()
            worker.RUNTIME["max_width"] = 10
            sys.modules["cv2"] = types.ModuleType("cv2")
            try:
                output = io.StringIO()
                with redirect_stdout(output):
                    worker.handle({"command": "analyze", "id": "limit-1", "path": source.name})
                response = json.loads(output.getvalue())
                self.assertEqual(response["error"]["code"], "resource_limit")
            finally:
                worker.MODEL = old_model
                worker.load_oriented_image = old_loader
                worker.RUNTIME["max_width"] = old_width
                if old_cv2 is None:
                    sys.modules.pop("cv2", None)
                else:
                    sys.modules["cv2"] = old_cv2

    def test_analyze_rejects_file_changed_during_inference(self):
        class Image:
            shape = (10, 10, 3)

        class Model:
            def __init__(self, path):
                self.path = path

            def get(self, _image):
                with open(self.path, "ab") as output:
                    output.write(b"changed")
                return []

        with tempfile.NamedTemporaryFile() as source:
            source.write(b"image")
            source.flush()
            expected_size = os.path.getsize(source.name)
            old_model = worker.MODEL
            old_loader = worker.load_oriented_image
            old_cv2 = sys.modules.get("cv2")
            worker.MODEL = Model(source.name)
            worker.load_oriented_image = lambda _path, _cv2, *_limits: Image()
            sys.modules["cv2"] = types.ModuleType("cv2")
            try:
                output = io.StringIO()
                with redirect_stdout(output):
                    worker.handle({"command": "analyze", "id": "changed-1", "path": source.name, "expected_size": expected_size})
                response = json.loads(output.getvalue())
                self.assertEqual(response["error"]["code"], "file_changed")
                self.assertTrue(response["error"]["retryable"])
            finally:
                worker.MODEL = old_model
                worker.load_oriented_image = old_loader
                if old_cv2 is None:
                    sys.modules.pop("cv2", None)
                else:
                    sys.modules["cv2"] = old_cv2

    def test_model_loader_passes_trimmed_modules_and_pipeline_configuration(self):
        captured = {}

        class FaceAnalysis:
            def __init__(self, **kwargs):
                captured.update(kwargs)

            def prepare(self, **kwargs):
                captured["prepare"] = kwargs

        package = types.ModuleType("insightface")
        app = types.ModuleType("insightface.app")
        app.FaceAnalysis = FaceAnalysis
        package.app = app
        old_modules = {name: sys.modules.get(name) for name in ("insightface", "insightface.app")}
        old_model, old_error = worker.MODEL, worker.MODEL_ERROR
        old_runtime = dict(worker.RUNTIME)
        worker.RUNTIME.update(model="buffalo_l", model_root="", providers=["CPUExecutionProvider"], modules=["detection", "recognition"], detector_size=(320, 320))
        sys.modules["insightface"] = package
        sys.modules["insightface.app"] = app
        try:
            worker.MODEL = None
            worker.MODEL_ERROR = None
            worker.load_model()
            self.assertEqual(captured["allowed_modules"], ["detection", "recognition"])
            self.assertEqual(captured["providers"], ["CPUExecutionProvider"])
            self.assertEqual(captured["prepare"]["det_size"], (320, 320))
            self.assertIsNone(worker.MODEL_ERROR)
        finally:
            worker.MODEL, worker.MODEL_ERROR = old_model, old_error
            worker.RUNTIME.clear()
            worker.RUNTIME.update(old_runtime)
            for name, value in old_modules.items():
                if value is None:
                    sys.modules.pop(name, None)
                else:
                    sys.modules[name] = value

    def test_oriented_loader_transposes_before_bgr_conversion(self):
        calls = []

        class Source:
            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

        class Oriented:
            def convert(self, mode):
                calls.append(("convert", mode))
                return self

        image_module = types.ModuleType("PIL.Image")
        image_module.MAX_IMAGE_PIXELS = 89
        image_module.open = lambda _path: Source()
        image_ops = types.ModuleType("PIL.ImageOps")
        image_ops.exif_transpose = lambda _source: (calls.append(("transpose",)) or Oriented())
        pil_package = types.ModuleType("PIL")
        pil_package.Image = image_module
        pil_package.ImageOps = image_ops
        numpy_module = types.ModuleType("numpy")
        numpy_module.asarray = lambda _image: "rgb-pixels"
        cv2_module = types.ModuleType("cv2")
        cv2_module.COLOR_RGB2BGR = "rgb2bgr"
        cv2_module.cvtColor = lambda pixels, mode: (calls.append(("cvt", pixels, mode)) or "bgr-pixels")
        heif_module = types.ModuleType("pillow_heif")
        heif_module.register_heif_opener = lambda: calls.append(("heif",))
        old_modules = {name: sys.modules.get(name) for name in ("PIL", "PIL.Image", "PIL.ImageOps", "numpy", "pillow_heif")}
        sys.modules.update({"PIL": pil_package, "PIL.Image": image_module, "PIL.ImageOps": image_ops, "numpy": numpy_module, "pillow_heif": heif_module})
        try:
            self.assertEqual(worker.load_oriented_image("photo.jpg", cv2_module, 100), "bgr-pixels")
            self.assertEqual(image_module.MAX_IMAGE_PIXELS, 110)
            self.assertEqual(calls, [("heif",), ("transpose",), ("convert", "RGB"), ("cvt", "rgb-pixels", "rgb2bgr")])
        finally:
            for name, value in old_modules.items():
                if value is None:
                    sys.modules.pop(name, None)
                else:
                    sys.modules[name] = value

    def test_oriented_loader_with_real_pillow_and_opencv(self):
        try:
            import cv2
            from PIL import Image
        except ImportError:
            self.skipTest("Pillow/OpenCV unavailable")
        with tempfile.NamedTemporaryFile(suffix=".jpg") as source:
            image = Image.new("RGB", (2, 3), (255, 0, 0))
            exif = Image.Exif()
            exif[274] = 6
            image.save(source.name, exif=exif)
            loaded = worker.load_oriented_image(source.name, cv2)
            self.assertEqual(tuple(loaded.shape[:2]), (2, 3))
            self.assertEqual(loaded.shape[2], 3)

    def test_real_worker_process_negotiates_when_model_is_installed(self):
        if importlib.util.find_spec("insightface") is None:
            self.skipTest("InsightFace unavailable")
        model_root = os.environ.get("FACE_ME_BRO_MODEL_ROOT", "")
        manifest = pathlib.Path(model_root) / "models" / "buffalo_l" / "checksums.json"
        if not model_root or not manifest.is_file():
            self.skipTest("buffalo_l model is not installed")
        command = [
            sys.executable,
            str(MODULE_PATH),
            "--model",
            "buffalo_l",
            "--model-root",
            model_root,
            "--providers",
            "CPUExecutionProvider",
            "--pipeline-id",
            "test-pipeline",
        ]
        process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

        def stop_process():
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
            for stream in (process.stdin, process.stdout, process.stderr):
                if stream is not None:
                    stream.close()

        self.addCleanup(stop_process)
        assert process.stdin is not None and process.stdout is not None
        process.stdin.write(json.dumps({"command": "hello", "id": "real-hello", "protocol_version": 1}) + "\n")
        process.stdin.flush()
        response = json.loads(process.stdout.readline())
        self.assertEqual(response["command"], "hello_ack")
        self.assertEqual(response["capabilities"]["pipeline_id"], "test-pipeline")
        self.assertIn("analyze", response["capabilities"]["supported_commands"])

    def test_real_worker_analyzes_oriented_image_when_model_is_installed(self):
        if importlib.util.find_spec("insightface") is None:
            self.skipTest("InsightFace unavailable")
        try:
            from PIL import Image
        except ImportError:
            self.skipTest("Pillow unavailable")
        model_root = os.environ.get("FACE_ME_BRO_MODEL_ROOT", "")
        manifest = pathlib.Path(model_root) / "models" / "buffalo_l" / "checksums.json"
        if not model_root or not manifest.is_file():
            self.skipTest("buffalo_l model is not installed")
        with tempfile.TemporaryDirectory() as directory:
            image_path = pathlib.Path(directory) / "oriented.jpg"
            image = Image.new("RGB", (20, 30), (255, 0, 0))
            exif = Image.Exif()
            exif[274] = 6
            image.save(image_path, exif=exif)
            data = image_path.read_bytes()
            command = [
                sys.executable, str(MODULE_PATH), "--model", "buffalo_l",
                "--model-root", model_root, "--providers", "CPUExecutionProvider",
                "--pipeline-id", "test-pipeline",
            ]
            process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

            def stop_process():
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                for stream in (process.stdin, process.stdout, process.stderr):
                    if stream is not None:
                        stream.close()

            self.addCleanup(stop_process)
            assert process.stdin is not None and process.stdout is not None
            process.stdin.write(json.dumps({"command": "hello", "id": "hello", "protocol_version": 1}) + "\n")
            process.stdin.flush()
            hello = json.loads(process.stdout.readline())
            self.assertEqual(hello["command"], "hello_ack")
            process.stdin.write(json.dumps({
                "command": "analyze", "id": "analyze", "path": str(image_path),
                "expected_size": len(data), "expected_sha256": hashlib.sha256(data).hexdigest(),
                "max_pixels": 100000,
            }) + "\n")
            process.stdin.flush()
            result = json.loads(process.stdout.readline())
            self.assertTrue(result.get("ok"), result)
            self.assertEqual(result["model"]["pipeline_id"], "test-pipeline")
            self.assertEqual((result["image"]["width"], result["image"]["height"]), (30, 20))
            self.assertIsInstance(result["faces"], list)


if __name__ == "__main__":
    unittest.main()
