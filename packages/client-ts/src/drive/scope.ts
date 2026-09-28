/** A handle limited to one folder plus picked nodes, including their descendants. */
import type { DriveFiles, OpenFile } from "./files.js";
import { normalizeName } from "./names.js";

export class DriveScope {
  readonly #picked: Set<string>;
  constructor(readonly files: DriveFiles, readonly root: string, picked: string[] = []) {
    this.#picked = new Set(picked);
  }
  private async allow(id: string): Promise<void> {
    const seen = new Set<string>();
    let current: string | undefined = id;
    while (current && !seen.has(current) && seen.size < 256) {
      if (current === this.root || this.#picked.has(current)) return;
      seen.add(current);
      current = (await this.files.client.node(current)).folder;
    }
    throw new Error("node is outside the scoped handle");
  }
  async open(node: string): Promise<OpenFile> {
    await this.allow(node);
    return this.files.open(node);
  }
  async resolve(path: string): Promise<OpenFile> {
    if (!this.root) throw new Error("scoped handle has no folder");
    let current = await this.files.open(this.root);
    if (path === "" || path === "/") return current;
    for (const part of path.replace(/^\//, "").split("/").filter(Boolean)) {
      const name = normalizeName(part);
      const next = (await this.files.list(current)).find(child => child.name === name);
      if (!next) throw new Error(`drive path not found: ${name}`);
      await this.allow(next.manifest.node);
      current = next;
    }
    return current;
  }
  async list(folder: OpenFile): Promise<OpenFile[]> {
    await this.allow(folder.manifest.node);
    return this.files.list(folder);
  }
  async create(parent: OpenFile, name: string, kind: "file" | "folder", bytes?: Uint8Array<ArrayBufferLike>): Promise<OpenFile> {
    await this.allow(parent.manifest.node);
    return this.files.create(parent, name, kind, bytes);
  }
  async replace(file: OpenFile, bytes: Uint8Array): Promise<void> {
    await this.allow(file.manifest.node);
    return this.files.replace(file, bytes);
  }
  async move(file: OpenFile, parent: OpenFile, name: string): Promise<void> {
    if (file.manifest.node === this.root) throw new Error("the scoped folder cannot be moved");
    await this.allow(file.manifest.node);
    await this.allow(parent.manifest.node);
    return this.files.move(file, parent, name);
  }
  async remove(file: OpenFile): Promise<void> {
    if (file.manifest.node === this.root) throw new Error("the scoped folder cannot be removed");
    await this.allow(file.manifest.node);
    return this.files.remove(file);
  }
  async append(file: OpenFile, plaintext: Uint8Array): Promise<number> {
    await this.allow(file.manifest.node);
    return this.files.append(file, plaintext);
  }
}
