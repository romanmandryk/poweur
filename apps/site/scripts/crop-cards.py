"""Crop the use-case card images out of the raw screenshots in assets/shots/.

Run after the product-shots Playwright spec: python3 apps/site/scripts/crop-cards.py
"""
from pathlib import Path

from PIL import Image

SHOTS = Path(__file__).resolve().parent.parent / "assets" / "shots"
CROPS = {
    # output name: (source screenshot, crop box in source pixels)
    "card-chat-crop": ("phone-bob-thread", (0, 300, 1170, 1400)),
    "card-share-crop": ("desktop-share-panel", (940, 180, 1560, 780)),
    "card-devices-crop": ("card-devices", (0, 600, 780, 1125)),
}

for name, (source, box) in CROPS.items():
    image = Image.open(SHOTS / f"{source}.png").crop(box)
    if image.width > 900:
        image = image.resize((900, round(image.height * 900 / image.width)), Image.Resampling.LANCZOS)
    image.save(SHOTS / f"{name}.png", optimize=True)
    print(name, image.size)
