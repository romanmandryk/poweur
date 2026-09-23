/**
 * App icons and splash screens (E19, from design/brand/scripts/icons.py). The
 * store rejects an iOS icon with an alpha channel, and an adaptive icon that
 * names a missing layer falls back to the platform default — both only show
 * up on a device, so they are pinned here.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const mobile = join(dirname(fileURLToPath(import.meta.url)), "..");
const appIcon = join(mobile, "ios/App/App/Assets.xcassets/AppIcon.appiconset");
const res = join(mobile, "android/app/src/main/res");
const DENSITIES = { mdpi: 1, hdpi: 1.5, xhdpi: 2, xxhdpi: 3, xxxhdpi: 4 };

/** Width, height and colour type from a PNG's IHDR chunk (colour types 4 and 6 carry alpha). */
function png(path) {
  const bytes = readFileSync(path);
  assert.equal(bytes.subarray(1, 4).toString("latin1"), "PNG", `${path} is not a PNG`);
  return { width: bytes.readUInt32BE(16), height: bytes.readUInt32BE(20), alpha: [4, 6].includes(bytes[25]) };
}

test("iOS: every icon the catalog names is 1024 px square with no alpha", () => {
  const { images } = JSON.parse(readFileSync(join(appIcon, "Contents.json"), "utf8"));
  assert.deepEqual(images.map((i) => i.appearances?.[0]?.value ?? "any"), ["any", "dark", "tinted"]);
  for (const { filename } of images) {
    assert.deepEqual(png(join(appIcon, filename)), { width: 1024, height: 1024, alpha: false }, filename);
  }
});

test("Android: the adaptive icon's layers exist at every density", () => {
  for (const name of ["ic_launcher.xml", "ic_launcher_round.xml"]) {
    const xml = readFileSync(join(res, "mipmap-anydpi-v26", name), "utf8");
    const layers = [...xml.matchAll(/@mipmap\/(\w+)/g)].map((m) => m[1]);
    assert.deepEqual(layers, ["ic_launcher_background", "ic_launcher_foreground", "ic_launcher_monochrome"], name);
    for (const [density, scale] of Object.entries(DENSITIES)) {
      for (const layer of layers) {
        const { width, height } = png(join(res, `mipmap-${density}`, `${layer}.png`));
        assert.deepEqual([width, height], [108 * scale, 108 * scale], `${density}/${layer}`);
      }
    }
  }
});

test("Android: legacy launcher icons are 48 dp with transparent corners", () => {
  for (const [density, scale] of Object.entries(DENSITIES)) {
    for (const name of ["ic_launcher.png", "ic_launcher_round.png"]) {
      const icon = png(join(res, `mipmap-${density}`, name));
      assert.deepEqual(icon, { width: 48 * scale, height: 48 * scale, alpha: true }, `${density}/${name}`);
    }
  }
});

test("splash screens exist for both platforms", () => {
  assert.ok(existsSync(join(mobile, "ios/App/App/Assets.xcassets/Splash.imageset/splash-2732x2732.png")));
  for (const orientation of ["port", "land"]) {
    for (const density of Object.keys(DENSITIES)) {
      assert.ok(existsSync(join(res, `drawable-${orientation}-${density}`, "splash.png")), `${orientation}-${density}`);
    }
  }
});
