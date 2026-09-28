import { mkdtemp, readFile, rm, writeFile, symlink, unlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, it } from "vitest";
import { FileChunkCache } from "../src/node/drive-cache.js";
import { chunkID } from "../src/drive/crypto.js";
it("persists verified ciphertext, rejects corruption and unsafe names", async () => {
  const directory = await mkdtemp(join(tmpdir(), "drive-cache-"));
  try {
    const cache = new FileChunkCache(directory), bytes = new Uint8Array([1,2,3]), id = chunkID(bytes);
    expect(await cache.get(id)).toBeNull();
    await cache.put(id, bytes);
    expect(await new FileChunkCache(directory).get(id)).toEqual(bytes);
    expect(new Uint8Array(await readFile(join(directory,id)))).toEqual(bytes);
    await writeFile(join(directory,id), "corrupt");
    expect(await cache.get(id)).toBeNull();
    await expect(cache.put(id, new Uint8Array([4]))).rejects.toThrow();
    for (const bad of ["../secret", "", "ABC"]) {
      await expect(cache.get(bad)).rejects.toThrow();
      await expect(cache.put(bad,bytes)).rejects.toThrow();
    }
    await unlink(join(directory,id)); await writeFile(join(directory,"target"),bytes); await symlink(join(directory,"target"),join(directory,id));
    expect(await cache.get(id)).toBeNull();
  } finally { await rm(directory,{ recursive:true,force:true }); }
});
