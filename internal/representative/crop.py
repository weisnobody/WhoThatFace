import base64
import io
import json
import sys

from PIL import Image, ImageOps

try:
    from pillow_heif import register_heif_opener
except ImportError:
    # The default worker environment installs pillow-heif. Keep the cropper
    # usable with a minimal Pillow environment for non-HEIF images.
    pass
else:
    register_heif_opener()


path, left, top, right, bottom = sys.argv[1], *(float(value) for value in sys.argv[2:])
image = ImageOps.exif_transpose(Image.open(path)).convert("RGBA")
width, height = image.size
box_width, box_height = right - left, bottom - top
left -= box_width * 0.20
top -= box_height * 0.20
right += box_width * 0.20
bottom += box_height * 0.20
square = max(right - left, bottom - top)
left -= (square - (right - left)) / 2
top -= (square - (bottom - top)) / 2
if left < 0:
    left = 0
if top < 0:
    top = 0
if left + square > width:
    left = width - square
if top + square > height:
    top = height - square
if square > width:
    left, square = 0, width
if square > height:
    top, square = 0, height
x0, y0 = max(0, int(left)), max(0, int(top))
x1, y1 = min(width, int(left + square)), min(height, int(top + square))
size = max(1, min(96, int(square)))
image = image.crop((x0, y0, x1, y1)).resize((size, size), Image.Resampling.LANCZOS)
output = io.BytesIO()
image.save(output, format="PNG")
print(json.dumps({
    "data": base64.b64encode(output.getvalue()).decode("ascii"),
    "width": size,
    "height": size,
    "mime_type": "image/png",
}))
