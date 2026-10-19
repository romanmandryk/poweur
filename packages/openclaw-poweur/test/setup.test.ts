import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { newRecoveryKit } from "@poweur/client";
import { IdentitySetupError } from "../src/identity.js";
import { explainSetupError, readRecoveryPhrase } from "../src/setup.js";

const input = { identity: "roman-claw.poweur.net", allowFrom: "roman.poweur.net" };

describe("explainSetupError", () => {
  it("a taken name: offers another name and the exact restore command", () => {
    const text = explainSetupError(new IdentitySetupError("taken", "roman-claw.poweur.net"), input);
    expect(text).toContain("roman-claw.poweur.net is already taken");
    expect(text).toContain("openclaw channels add --channel poweur --identity roman-claw-agent.poweur.net --allow-from roman.poweur.net");
    expect(text).toContain("--identity roman-claw.poweur.net --allow-from roman.poweur.net --recovery-phrase-file ~/agent-phrase.txt");
    expect(text).toContain("~/.poweur");
  });

  it("a wrong phrase and a refused name say so", () => {
    expect(explainSetupError(new IdentitySetupError("phrase-mismatch", "roman-claw.poweur.net"), input)).toMatch(/belongs to a different ID/);
    const refused = explainSetupError(new IdentitySetupError("refused", "bot.poweur.net", 'handle "bot" is reserved'), { ...input, identity: "bot.poweur.net" });
    expect(refused).toContain('handle "bot" is reserved');
    expect(refused).toContain("--identity bot-agent.poweur.net");
  });
});

describe("readRecoveryPhrase", () => {
  it("reads the phrase from the file, else the environment, else nothing", () => {
    const seed = new Uint8Array(32).fill(7);
    const phrase = newRecoveryKit("x.poweur.net", "", seed).mnemonic;
    const file = join(mkdtempSync(join(tmpdir(), "phrase-")), "p.txt");
    writeFileSync(file, `  ${phrase}\n`);
    expect(readRecoveryPhrase({ recoveryPhraseFile: file }, {})).toEqual(seed);
    expect(readRecoveryPhrase({}, { POWEUR_RECOVERY_PHRASE: phrase })).toEqual(seed);
    expect(readRecoveryPhrase({}, {})).toBeUndefined();
    expect(() => readRecoveryPhrase({}, { POWEUR_RECOVERY_PHRASE: "not a phrase" })).toThrow(/24 words/);
  });
});
