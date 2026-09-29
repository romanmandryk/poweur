/** Storage-v2 Files destination: encrypted files, direct shares and mounts. */
import { useEffect, useMemo, useRef, useState } from "react";
import { Download, File as FileIcon, Folder, FolderPlus, Share2, Trash2, Upload } from "lucide-react";
import type { DriveFiles, OpenFile, Share, ShareRole } from "@poweur/client/drive";
import { acceptBrowserOffer, loadMounts, revokeBrowserShare, shareBrowserFile, shareOffers, sharesForFile } from "../../actions/files";
import { askConfirm, askText } from "../../components/Dialogs";
import { openBrowserDrive, readFileBytes } from "../../lib/drive";
import { errorMessage } from "../../actions/relay";
import { useData } from "../../state/data";
import { useSession } from "../../state/session";
import { openPanel, setLoading, toast } from "../../state/ui";
import { Button, IconButton } from "../../ui/Button";
import { EmptyState, Notice } from "../../ui/Display";
import { FormGroup, Input, Label } from "../../ui/Field";
import { DestHeader } from "../../ui/Layout";

type View = { files: DriveFiles; folder: OpenFile; label: string; trail: { file: OpenFile; label: string }[] };

export function Files() {
  const identity = useSession((state) => state.identity)!;
  const messages = useData((state) => state.messages);
  const offers = useMemo(() => shareOffers(messages, identity), [identity, messages]);
  const [view, setView] = useState<View | null>(null);
  const [entries, setEntries] = useState<OpenFile[]>([]);
  const [mounts, setMounts] = useState<Awaited<ReturnType<typeof loadMounts>>>([]);
  const [tab, setTab] = useState<"mine" | "shared">("mine");
  const [error, setError] = useState("");
  const input = useRef<HTMLInputElement>(null);

  async function show(next: View) {
    setLoading(true, "Opening files…");
    try {
      const children = await next.files.list(next.folder);
      const visible = next.trail.length === 1 && next.files.client.drive === identity
        ? children.filter((file) => file.name !== ".poweur")
        : children;
      setEntries(visible.sort((a, b) => Number(b.manifest.kind === "folder") - Number(a.manifest.kind === "folder") || a.name.localeCompare(b.name)));
      setView(next);
      setError("");
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setLoading(false);
    }
  }

  async function openMine() {
    try {
      const own = await openBrowserDrive(identity);
      const root = await own.files.root();
      await show({ files: own.files, folder: root, label: "Files", trail: [{ file: root, label: "Files" }] });
    } catch (cause) {
      setError(errorMessage(cause));
    }
  }

  async function refreshShared() {
    try {
      setMounts(await loadMounts(identity));
      setError("");
    } catch (cause) {
      setError(errorMessage(cause));
    }
  }

  useEffect(() => {
    // Both calls may initialize the encrypted root on a brand-new drive, so
    // serialize them instead of racing two root creates.
    void openMine().then(refreshShared);
  }, [identity]);

  async function enter(file: OpenFile) {
    if (!view) return;
    if (file.manifest.kind === "folder") {
      await show({ ...view, folder: file, label: file.name || view.label, trail: [...view.trail, { file, label: file.name || view.label }] });
      return;
    }
    await download(file);
  }

  async function download(file: OpenFile) {
    if (!view) return;
    return downloadFrom(view.files, file, file.name);
  }

  async function downloadFrom(files: DriveFiles, file: OpenFile, displayName = "") {
    setLoading(true, `Decrypting ${file.name || "file"}…`);
    try {
      const bytes = await readFileBytes(files, file);
      const url = URL.createObjectURL(new Blob([Uint8Array.from(bytes).buffer]));
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = file.name || displayName || "download";
      anchor.click();
      URL.revokeObjectURL(url);
    } catch (cause) {
      toast(errorMessage(cause), "error", 7000);
    } finally {
      setLoading(false);
    }
  }

  async function upload(file: File) {
    if (!view) return;
    setLoading(true, `Encrypting ${file.name}…`);
    try {
      const bytes = new Uint8Array(await file.arrayBuffer());
      const existing = entries.find((entry) => entry.name === file.name);
      if (existing) {
        if (existing.manifest.kind !== "file") throw new Error("a folder already uses that name");
        await view.files.replace(existing, bytes);
      } else {
        await view.files.create(view.folder, file.name, "file", bytes);
      }
      await show(view);
      toast(existing ? "File replaced" : "File uploaded", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error", 7000);
    } finally {
      if (input.current) input.current.value = "";
      setLoading(false);
    }
  }

  async function newFolder() {
    if (!view) return;
    const name = await askText({ title: "New folder", label: "Folder name", confirmLabel: "Create" });
    if (!name) return;
    setLoading(true, "Creating folder…");
    try {
      await view.files.create(view.folder, name, "folder");
      await show(view);
    } catch (cause) {
      toast(errorMessage(cause), "error", 7000);
    } finally {
      setLoading(false);
    }
  }

  async function remove(file: OpenFile) {
    if (!view || !await askConfirm({ title: `Delete ${file.name}?`, message: "This removes it from every synced device.", confirmLabel: "Delete" })) return;
    setLoading(true, "Deleting…");
    try {
      await view.files.remove(file);
      await show(view);
      toast("Deleted", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error", 7000);
    } finally {
      setLoading(false);
    }
  }

  async function openMount(mount: (typeof mounts)[number]) {
    setLoading(true, "Opening shared files…");
    try {
      const shared = await openBrowserDrive(identity, mount.drive, mount.relay);
      const root = await shared.files.open(mount.node);
      const label = mount.name || mount.drive;
      setTab("shared");
      if (root.manifest.kind === "file") {
        await downloadFrom(shared.files, root, label);
        return;
      }
      await show({ files: shared.files, folder: root, label, trail: [{ file: root, label }] });
    } catch (cause) {
      toast(errorMessage(cause), "error", 7000);
    } finally {
      setLoading(false);
    }
  }

  async function accept(offer: (typeof offers)[number]) {
    setLoading(true, "Verifying share…");
    try {
      const mount = await acceptBrowserOffer(identity, offer);
      await refreshShared();
      toast("Share added to Files", "success");
      await openMount(mount);
    } catch (cause) {
      toast(errorMessage(cause), "error", 7000);
    } finally {
      setLoading(false);
    }
  }

  const atOwnRoot = tab === "mine" && view?.trail.length === 1;
  return (
    <>
      <DestHeader title={view?.label || "Files"}>
        {tab === "mine" && view && (
          <>
            <IconButton id="btn-new-folder" aria-label="New folder" onClick={() => void newFolder()}><FolderPlus className="size-5" /></IconButton>
            <IconButton id="btn-upload-file" aria-label="Upload file" onClick={() => input.current?.click()}><Upload className="size-5" /></IconButton>
            <input ref={input} id="file-upload-input" className="hidden" type="file" onChange={(event) => event.currentTarget.files?.[0] && void upload(event.currentTarget.files[0])} />
          </>
        )}
      </DestHeader>

      <div role="tablist" aria-label="Files view" className="mx-4 mb-3 grid grid-cols-2 rounded-control bg-surface-2 p-1">
        <button role="tab" aria-selected={tab === "mine"} className={`min-h-10 rounded-lg text-sm font-semibold ${tab === "mine" ? "bg-surface text-accent shadow-sm" : "text-muted"}`} onClick={() => { setTab("mine"); void openMine(); }}>My files</button>
        <button role="tab" aria-selected={tab === "shared"} className={`min-h-10 rounded-lg text-sm font-semibold ${tab === "shared" ? "bg-surface text-accent shadow-sm" : "text-muted"}`} onClick={() => { setTab("shared"); setView(null); void refreshShared(); }}>Shared with me</button>
      </div>

      {error && <Notice tone="warn" className="mx-4">{error}</Notice>}

      {view && view.trail.length > 1 && (
        <nav aria-label="Folder path" className="flex gap-1 overflow-x-auto px-4 pb-2 text-sm text-muted">
          {view.trail.map((part, index) => <button key={part.file.manifest.node} className="shrink-0 text-accent" onClick={() => void show({ ...view, folder: part.file, label: part.label, trail: view.trail.slice(0, index + 1) })}>{index ? `/ ${part.label}` : part.label}</button>)}
        </nav>
      )}

      {tab === "shared" && !view && (
        <SharedList offers={offers.filter((offer) => !mounts.some((mount) => mount.share_id === offer.share.id))} mounts={mounts} onAccept={accept} onOpen={openMount} />
      )}

      {view && entries.length === 0 && (
        <EmptyState icon={atOwnRoot ? Upload : Folder} title={atOwnRoot ? "Your drive is empty" : "This folder is empty"} body={atOwnRoot ? "Upload a file or create a folder. Names and contents are encrypted before they leave this device." : undefined} action={atOwnRoot ? <Button className="w-auto px-6" onClick={() => input.current?.click()}>Upload a file</Button> : undefined} />
      )}

      {view && entries.length > 0 && (
        <div className="mx-4 overflow-hidden rounded-card bg-surface">
          {entries.map((file) => <FileRow key={file.manifest.node} file={file} own={tab === "mine"} onOpen={() => void enter(file)} onShare={() => openShareDialog(identity, file)} onDelete={() => void remove(file)} />)}
        </div>
      )}
    </>
  );
}

function FileRow({ file, own, onOpen, onShare, onDelete }: { file: OpenFile; own: boolean; onOpen: () => void; onShare: () => void; onDelete: () => void }) {
  const Icon = file.manifest.kind === "folder" ? Folder : FileIcon;
  return <div className="flex min-h-14 items-center gap-2 border-b border-sep px-3 last:border-0">
    <button className="flex min-w-0 flex-1 items-center gap-3 py-3 text-left" onClick={onOpen}><Icon className="size-5 shrink-0 text-accent" /><span className="truncate font-medium">{file.name || "Shared item"}</span></button>
    {file.manifest.kind === "file" && <IconButton aria-label={`Download ${file.name}`} onClick={onOpen}><Download className="size-4" /></IconButton>}
    {own && <IconButton aria-label={`Share ${file.name}`} onClick={onShare}><Share2 className="size-4" /></IconButton>}
    {own && <IconButton aria-label={`Delete ${file.name}`} className="text-danger" onClick={onDelete}><Trash2 className="size-4" /></IconButton>}
  </div>;
}

function SharedList({ offers, mounts, onAccept, onOpen }: { offers: ShareOfferLike[]; mounts: Awaited<ReturnType<typeof loadMounts>>; onAccept: (offer: ShareOfferLike) => void; onOpen: (mount: (typeof mounts)[number]) => void }) {
  if (!offers.length && !mounts.length) return <EmptyState icon={Share2} title="Nothing shared yet" body="Files and folders people share with you will appear here after you accept them." />;
  return <div className="mx-4 space-y-4">
    {offers.length > 0 && <section><h2 className="mb-2 text-sm font-bold text-muted">Offers</h2><div className="overflow-hidden rounded-card bg-surface">{offers.map((offer) => <div key={offer.share.id} className="flex items-center gap-3 border-b border-sep p-3 last:border-0"><Share2 className="size-5 text-accent" /><div className="min-w-0 flex-1"><div className="truncate font-semibold">{offer.name || "Shared item"}</div><div className="truncate text-sm text-muted">from {offer.share.issuer} · {offer.share.role}</div></div><Button size="sm" onClick={() => onAccept(offer)}>Accept</Button></div>)}</div></section>}
    {mounts.length > 0 && <section><h2 className="mb-2 text-sm font-bold text-muted">Mounted</h2><div className="overflow-hidden rounded-card bg-surface">{mounts.map((mount) => <button key={`${mount.drive}:${mount.node}`} className="flex min-h-14 w-full items-center gap-3 border-b border-sep p-3 text-left last:border-0" onClick={() => onOpen(mount)}><Folder className="size-5 text-accent" /><span className="min-w-0 flex-1"><span className="block truncate font-semibold">{mount.name || "Shared item"}</span><span className="block truncate text-sm text-muted">{mount.drive} · {mount.role}</span></span></button>)}</div></section>}
  </div>;
}

type ShareOfferLike = ReturnType<typeof shareOffers>[number];

function openShareDialog(identity: string, file: OpenFile) {
  openPanel(`Share ${file.name}`, (close) => <ShareForm identity={identity} file={file} close={close} />);
}

function ShareForm({ identity, file, close }: { identity: string; file: OpenFile; close: () => void }) {
  const field = useRef<HTMLInputElement>(null);
  const [role, setRole] = useState<ShareRole>("read");
  const [busy, setBusy] = useState(false);
  const [shares, setShares] = useState<Share[]>([]);
  useEffect(() => { void sharesForFile(identity, file).then(setShares, () => {}); }, [identity, file]);
  return <form onSubmit={(event) => { event.preventDefault(); const member = field.current?.value.trim(); if (!member) return; setBusy(true); void shareBrowserFile(identity, file, member, role).then(({ notified }) => { toast(notified ? `Shared with ${member}` : `Access granted, but ${member} could not be notified`, notified ? "success" : "warning", 7000); close(); }, (cause) => { toast(errorMessage(cause), "error", 7000); setBusy(false); }); }}>
    <FormGroup><Label htmlFor="share-member">Poweur ID</Label><Input ref={field} id="share-member" autoComplete="off" placeholder="alex.example.com" /></FormGroup>
    <FormGroup><Label htmlFor="share-role">Access</Label><select id="share-role" value={role} onChange={(event) => setRole(event.currentTarget.value as ShareRole)} className="input w-full rounded-control border-[1.5px] border-sep bg-surface px-4 py-[13px]"><option value="read">Can view</option><option value="write">Can edit</option><option value="admin">Can manage</option></select></FormGroup>
    <Notice>The recipient gets a signed offer. The relay never receives this file's decryption key in plaintext.</Notice>
    {shares.length > 0 && <div className="mb-4"><Label>People with access</Label><div className="overflow-hidden rounded-control bg-surface-2">{shares.map((share) => <div key={share.id} className="flex items-center gap-2 border-b border-sep px-3 py-2 last:border-0"><span className="min-w-0 flex-1 truncate text-sm">{share.member} · {share.role}</span><Button variant="danger" size="sm" data-revoke-share={share.id} onClick={() => { setBusy(true); void revokeBrowserShare(identity, share).then(() => { setShares((current) => current.filter((entry) => entry.id !== share.id)); toast("Access revoked", "success"); setBusy(false); }, (cause) => { toast(errorMessage(cause), "error", 7000); setBusy(false); }); }}>Revoke</Button></div>)}</div></div>}
    <div className="flex gap-2"><Button variant="secondary" className="min-h-11 flex-1" onClick={close}>Cancel</Button><Button type="submit" className="min-h-11 flex-1 p-3 text-[15px]" disabled={busy}>{busy ? "Sharing…" : "Share"}</Button></div>
  </form>;
}
