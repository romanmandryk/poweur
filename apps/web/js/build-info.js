/**
 * App release identity (EPIC-013 E13-T6).
 *
 * The web client has no bundler, so this file is the version the Settings →
 * About screen can import. Bump APP_VERSION (patch) and stamp APP_BUILD_TIME
 * (UTC `YYYY-MM-DD HH:MM`) in the same change set as `package.json`.
 */
export const APP_VERSION = "0.1.7";
export const APP_BUILD_TIME = "2026-09-10 15:35";
