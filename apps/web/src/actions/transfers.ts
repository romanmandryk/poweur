/** Browser composition for E05-T7's encrypted Send flow. */
import { createTransfer, expiredTransfers, revokeTransfer, type TransferSource, type TransferState } from "@poweur/client/drive";
import { openBrowserDrive } from "../lib/drive";
import { loadSnapshot, saveSnapshot } from "../lib/snapshot";
import { relayUrlFor } from "../lib/storage";
import { shareBrowserFile } from "./files";

const snapshotName = "transfers";

export function browserTransferSource(file: File): TransferSource {
  return {
    name: file.name,
    size: file.size,
    modified: file.lastModified,
    async slice(start, end) { return new Uint8Array(await file.slice(start, end).arrayBuffer()); },
  };
}

export async function loadBrowserTransfers(identity: string): Promise<TransferState[]> {
  return await loadSnapshot<TransferState[]>(identity, snapshotName) ?? [];
}

async function storeTransfer(identity: string, state: TransferState): Promise<void> {
  const current = await loadBrowserTransfers(identity);
  const next = [state, ...current.filter(item => item.id !== state.id)]
    .sort((a, b) => b.created_at.localeCompare(a.created_at)).slice(0, 50);
  await saveSnapshot(identity, snapshotName, next);
}

function browserOrigin(identity: string): string {
  const relay = new URL(relayUrlFor(identity));
  return `${relay.protocol}//${identity}${relay.port ? `:${relay.port}` : ""}`;
}

export async function sendBrowserTransfer(identity: string, selected: File[], options: {
  expiresAt?: string;
  password?: string;
  maxDownloads?: number;
  message?: string;
  recipients?: string[];
  state?: TransferState;
  onState?: (state: TransferState) => void;
}): Promise<{ state: TransferState; notified: number }> {
  const { files } = await openBrowserDrive(identity);
  const state = await createTransfer(files, selected.map(browserTransferSource), {
    origin: browserOrigin(identity),
    expiresAt: options.expiresAt,
    password: options.password,
    maxDownloads: options.maxDownloads,
    message: options.message,
    state: options.state,
    async onState(next) { await storeTransfer(identity, next); options.onState?.(next); },
  });
  const folder = await files.open(state.folder!);
  let notified = 0;
  for (const recipient of [...new Set(options.recipients ?? [])]) {
    const result = await shareBrowserFile(identity, folder, recipient, "read", state.expires_at);
    if (result.notified) notified++;
  }
  return { state, notified };
}

export async function revokeBrowserTransfer(identity: string, state: TransferState): Promise<TransferState> {
  const { files } = await openBrowserDrive(identity);
  const revoked = await revokeTransfer(files, state);
  await storeTransfer(identity, revoked);
  return revoked;
}

/**
 * Release transfers whose links have expired: their files are deleted and
 * leave the owner's usage. Runs when Files opens; the relay cannot delete an
 * owner's encrypted nodes itself. Returns how many were released.
 */
export async function expireBrowserTransfers(identity: string): Promise<number> {
  const due = expiredTransfers(await loadBrowserTransfers(identity));
  if (!due.length) return 0;
  const { files } = await openBrowserDrive(identity);
  let released = 0;
  for (const state of due) {
    try {
      await storeTransfer(identity, await revokeTransfer(files, state, "expired"));
      released++;
    } catch {
      // Tried again next time Files opens.
    }
  }
  return released;
}

export async function browserTransferStats(identity: string, state: TransferState) {
  if (!state.share?.link) throw new Error("transfer has no public link");
  const { drive } = await openBrowserDrive(identity);
  return drive.linkStats(state.share.link);
}
