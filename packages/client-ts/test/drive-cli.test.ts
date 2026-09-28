import { expect, it } from "vitest";
import { run } from "../src/cli/index.js";
it.each([[], ["unknown"], ["node"], ["info", "extra"], ["changes", "--limit=0"]])("rejects invalid drive arguments %j", async (...args) => {
  let error = "";
  expect(await run(["drive", ...args], { stdout: () => {}, stderr: text => { error += text; } })).toBe(1);
  expect(error).not.toBe("");
});
