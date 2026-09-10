#!/usr/bin/env node
/**
 * Install the debug APK on a USB phone or emulator and launch it.
 * Expects `:app:assembleDebug` to have already produced app-debug.apk.
 */
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const mobile = join(dirname(fileURLToPath(import.meta.url)), "..");
const apk = join(mobile, "android/app/build/outputs/apk/debug/app-debug.apk");
const appId = "net.poweur.app";
const activity = `${appId}/.MainActivity`;

function run(cmd, args, opts = {}) {
  const result = spawnSync(cmd, args, { stdio: "inherit", ...opts });
  if (result.error && result.error.code === "ENOENT") {
    console.error(`${cmd} is not on PATH.`);
    if (cmd === "adb") {
      console.error("Install Android platform-tools (Android Studio SDK Manager) and add them to PATH.");
    }
    process.exit(1);
  }
  return result.status ?? 1;
}

if (!existsSync(apk)) {
  console.error(`No debug APK at ${apk}`);
  console.error("Run `pnpm --filter @poweur/mobile run build:android` first.");
  process.exit(1);
}

const devices = spawnSync("adb", ["devices"], { encoding: "utf8" });
if (devices.error?.code === "ENOENT") {
  console.error("adb is not on PATH. Install Android platform-tools and add them to PATH.");
  process.exit(1);
}
if (devices.status !== 0) {
  process.exit(devices.status ?? 1);
}

const ready = (devices.stdout || "")
  .split("\n")
  .map((line) => line.trim())
  .filter((line) => line && !line.startsWith("List of devices"))
  .filter((line) => /\tdevice$/.test(line))
  .map((line) => line.split("\t")[0]);

if (ready.length === 0) {
  console.error("No Android device or emulator is connected (`adb devices` is empty).");
  console.error("Plug in a phone with USB debugging, or start an emulator, then retry.");
  process.exit(1);
}

const serial = process.env.ANDROID_SERIAL;
const adbArgs = serial ? ["-s", serial] : [];
if (!serial && ready.length > 1) {
  console.error(`Several devices are connected: ${ready.join(", ")}`);
  console.error("Set ANDROID_SERIAL to the one you want, e.g. ANDROID_SERIAL=" + ready[0]);
  process.exit(1);
}

const target = serial || ready[0];
console.log(`Installing on ${target}`);
let code = run("adb", [...adbArgs, "install", "-r", apk]);
if (code !== 0) process.exit(code);
code = run("adb", [...adbArgs, "shell", "am", "start", "-n", activity]);
process.exit(code);
