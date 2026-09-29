/** Storage-v2 Files destination: encrypted files, direct shares and mounts. */
import { useEffect, useMemo, useRef, useState } from "react";
import { Copy, Download, File as FileIcon, Folder, FolderPlus, Link2, Share2, Trash2, Upload } from "lucide-react";
import type { DriveFiles, OpenFile, Share, ShareRole } from "@poweur/client/drive";
import { acceptBrowserOffer, cachedBrowserFolder, ensureBrowserFiles, fileRequestBrowserLink, linkBrowserFile, loadBrowserFolder, loadMounts, refreshBrowserFiles, revokeBrowserShare, shareBrowserFile, shareOffers, sharesForFile } from "../../actions/files";
import { askConfirm, askText } from "../../components/Dialogs";
import { openBrowserDrive, readFileBytes } from "../../lib/drive";
import { errorMessage } from "../../actions/relay";
import { useData } from "../../state/data";
import { useSession } from "../../state/session";
import { openPanel, toast } from "../../state/ui";
import { Button, IconButton } from "../../ui/Button";
import { EmptyState, Notice } from "../../ui/Display";
import { FormGroup, Input, Label } from "../../ui/Field";
import { DestHeader } from "../../ui/Layout";
import { PullToRefresh } from "../../ui/PullToRefresh";
import { Tab, TabBar } from "../../ui/Tabs";

type View = { files: DriveFiles; folder: OpenFile; label: string; trail: { file: OpenFile; label: string }[] };

export function Files() {
  const identity = useSession((state) => state.identity)!;
  const messages = useData((state) => state.messages);
  const fileCache = useData((state) => state.files);
  const offers = useMemo(() => shareOffers(messages, identity), [identity, messages]);
  const tab = useData((state) => state.filesTab);
  // Each tab keeps its own place, as message trays do: switching back returns
  // to the folder you left. Tapping the active tab again goes to its top.
  const [views, setViews] = useState<{ mine: View | null; shared: View | null }>(() => ({
    mine: fileCache.own ? { files: fileCache.own.files, folder: fileCache.own.root, label: "Files", trail: [{ file: fileCache.own.root, label: "Files" }] } : null,
    shared: null,
  }));
  const view = views[tab];
  const setView = (next: View | null, which = tab) => setViews((current) => ({ ...current, [which]: next }));
  const selectTab = (next: typeof tab) => {
    if (next === tab) {
      if (next === "mine") void openMine();
      else setView(null, "shared");
      return;
    }
    useData.setState({ filesTab: next });
  };
  const [error, setError] = useState("");
  const [folderLoading, setFolderLoading] = useState(false);
  const [working, setWorking] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const entries = view ? cachedBrowserFolder(view.files, view.folder) ?? [] : [];
  const mounts = fileCache.mounts;

  async function show(next: View, which = tab) {
    setView(next, which);
    if (cachedBrowserFolder(next.files, next.folder)) return;
    setFolderLoading(true);
    try {
      await loadBrowserFolder(identity, next.files, next.folder);
      setError("");
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setFolderLoading(false);
    }
  }

  async function openMine() {
    const own = useData.getState().files.own;
    if (own) await show({ files: own.files, folder: own.root, label: "Files", trail: [{ file: own.root, label: "Files" }] }, "mine");
    else await ensureBrowserFiles(identity);
  }

  useEffect(() => {
    void ensureBrowserFiles(identity);
  }, [identity]);

  useEffect(() => {
    if (views.mine || !fileCache.own) return;
    setView({ files: fileCache.own.files, folder: fileCache.own.root, label: "Files", trail: [{ file: fileCache.own.root, label: "Files" }] }, "mine");
  }, [fileCache.own, views.mine]);

  async function refresh() {
    try { await refreshBrowserFiles(identity); setError(""); }
    catch (cause) { setError(errorMessage(cause)); }
  }

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
    setWorking(true);
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
      setWorking(false);
    }
  }

  async function upload(file: File) {
    if (!view) return;
    setWorking(true);
    try {
      const bytes = new Uint8Array(await file.arrayBuffer());
      const existing = entries.find((entry) => entry.name === file.name);
      if (existing) {
        if (existing.manifest.kind !== "file") throw new Error("a folder already uses that name");
        await view.files.replace(existing, bytes);
      } else {
        await view.files.create(view.folder, file.name, "file", bytes);
      }
      await loadBrowserFolder(identity, view.files, view.folder, true);
      toast(existing ? "File replaced" : "File uploaded", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error", 7000);
    } finally {
      if (input.current) input.current.value = "";
      setWorking(false);
    }
  }

  async function newFolder() {
    if (!view) return;
    const name = await askText({ title: "New folder", label: "Folder name", confirmLabel: "Create" });
    if (!name) return;
    setWorking(true);
    try {
      await view.files.create(view.folder, name, "folder");
      await loadBrowserFolder(identity, view.files, view.folder, true);
    } catch (cause) {
      toast(errorMessage(cause), "error", 7000);
    } finally {
      setWorking(false);
    }
  }

  async function remove(file: OpenFile) {
    if (!view || !await askConfirm({ title: `Delete ${file.name}?`, message: "This removes it from every synced device.", confirmLabel: "Delete" })) return;
    setWorking(true);
    try {
      await view.files.remove(file);
      await loadBrowserFolder(identity, view.files, view.folder, true);
      toast("Deleted", "success");
    } catch (cause) {
      toast(errorMessage(cause), "error", 7000);
    } finally {
      setWorking(false);
    }
  }

  async function openMount(mount: (typeof mounts)[number]) {
    setFolderLoading(true);
    try {
      const shared = await openBrowserDrive(identity, mount.drive, mount.relay);
      const root = await shared.files.open(mount.node);
      const label = mount.name || mount.drive;
      if (root.manifest.kind === "file") {
        await downloadFrom(shared.files, root, label);
        return;
      }
      useData.setState({ filesTab: "shared" });
      await show({ files: shared.files, folder: root, label, trail: [{ file: root, label }] }, "shared");
    } catch (cause) {
      toast(errorMessage(cause), "error", 7000);
    } finally {
      setFolderLoading(false);
    }
  }

  async function accept(offer: (typeof offers)[number]) {
    setWorking(true);
    try {
      const mount = await acceptBrowserOffer(identity, offer);
      await refreshBrowserFiles(identity);
      toast("Share added to Files", "success");
      await openMount(mount);
    } catch (cause) {
      toast(errorMessage(cause), "error", 7000);
    } finally {
      setWorking(false);
    }
  }

  const atOwnRoot = tab === "mine" && view?.trail.length === 1;
  return (
    <>
      <PullToRefresh onRefresh={refresh} busy={fileCache.loading || folderLoading || working}>
      <DestHeader title="Files">
        {tab === "mine" && view && (
          <>
            <IconButton id="btn-new-folder" aria-label="New folder" onClick={() => void newFolder()}><FolderPlus className="size-5" /></IconButton>
            <IconButton id="btn-upload-file" aria-label="Upload file" onClick={() => input.current?.click()}><Upload className="size-5" /></IconButton>
            <input ref={input} id="file-upload-input" className="hidden" type="file" onChange={(event) => event.currentTarget.files?.[0] && void upload(event.currentTarget.files[0])} />
          </>
        )}
      </DestHeader>

      <TabBar className="tray-bar" aria-label="Files view">
        <Tab data-files-tab="mine" active={tab === "mine"} onClick={() => selectTab("mine")}>My files</Tab>
        <Tab data-files-tab="shared" active={tab === "shared"} onClick={() => selectTab("shared")}>Shared with me</Tab>
      </TabBar>
      {(error || fileCache.error) && <Notice tone="warn" className="mx-4 mt-3">{error || fileCache.error}</Notice>}

      {view && (tab === "shared" || view.trail.length > 1) && (
        <nav aria-label="Folder path" className="flex gap-1 overflow-x-auto px-4 pt-3 pb-2 text-sm text-muted">
          {/* Inside a shared item, the way back to everything shared with you. */}
          {tab === "shared" && <button className="shrink-0 text-accent" onClick={() => setView(null, "shared")}>Shared with me</button>}
          {view.trail.map((part, index) => <button key={part.file.manifest.node} className="shrink-0 text-accent" onClick={() => void show({ ...view, folder: part.file, label: part.label, trail: view.trail.slice(0, index + 1) })}>{index || tab === "shared" ? `/ ${part.label}` : part.label}</button>)}
        </nav>
      )}

      {tab === "mine" && !view && fileCache.preview && fileCache.preview.length > 0 && (
        // Last seen on this device, shown while the drive opens.
        <div className="conv-list file-list bg-surface" aria-busy="true" data-files-preview>
          {fileCache.preview.map((entry) => {
            const Icon = entry.kind === "folder" ? Folder : FileIcon;
            return <div key={entry.node} className="file-row flex min-h-13 items-center gap-3 border-b border-sep px-4 py-3 opacity-70 last:border-b-0"><span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-surface-2 text-accent"><Icon className="size-[18px]" aria-hidden="true" /></span><span className="truncate text-[15px] font-semibold">{entry.name}</span></div>;
          })}
        </div>
      )}

      {tab === "shared" && !view && !fileCache.loading && (
        <SharedList offers={offers.filter((offer) => !mounts.some((mount) => mount.share_id === offer.share.id))} mounts={mounts} onAccept={accept} onOpen={openMount} />
      )}

      {view && entries.length === 0 && !fileCache.loading && !folderLoading && !working && (
        <EmptyState icon={atOwnRoot ? Upload : Folder} title={atOwnRoot ? "Your drive is empty" : "This folder is empty"} body={atOwnRoot ? "Upload a file or create a folder. Names and contents are encrypted before they leave this device." : undefined} action={atOwnRoot ? <Button className="w-auto px-6" onClick={() => input.current?.click()}>Upload a file</Button> : undefined} />
      )}

      {view && entries.length > 0 && (
        <div className="conv-list file-list bg-surface">
          {entries.map((file) => <FileRow key={file.manifest.node} file={file} own={tab === "mine"} onOpen={() => void enter(file)} onShare={() => openShareDialog(identity, file)} onDelete={() => void remove(file)} />)}
        </div>
      )}
      </PullToRefresh>
    </>
  );
}

function FileRow({ file, own, onOpen, onShare, onDelete }: { file: OpenFile; own: boolean; onOpen: () => void; onShare: () => void; onDelete: () => void }) {
  const Icon = file.manifest.kind === "folder" ? Folder : FileIcon;
  return <div className="file-row flex min-h-13 items-center gap-2 border-b border-sep px-4 transition-colors last:border-b-0 [@media(hover:hover)]:hover:bg-surface-2">
    <button className="flex min-w-0 flex-1 items-center gap-3 py-3 text-left" onClick={onOpen}><span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-surface-2 text-accent"><Icon className="size-[18px]" aria-hidden="true" /></span><span className="truncate text-[15px] font-semibold">{file.name || "Shared item"}</span></button>
    {file.manifest.kind === "file" && <IconButton aria-label={`Download ${file.name}`} onClick={onOpen}><Download className="size-4" /></IconButton>}
    {own && <IconButton aria-label={`Share ${file.name}`} onClick={onShare}><Share2 className="size-4" /></IconButton>}
    {own && <IconButton aria-label={`Delete ${file.name}`} className="text-danger" onClick={onDelete}><Trash2 className="size-4" /></IconButton>}
  </div>;
}

function SharedList({ offers, mounts, onAccept, onOpen }: { offers: ShareOfferLike[]; mounts: Awaited<ReturnType<typeof loadMounts>>; onAccept: (offer: ShareOfferLike) => void; onOpen: (mount: (typeof mounts)[number]) => void }) {
  if (!offers.length && !mounts.length) return <EmptyState icon={Share2} title="Nothing shared yet" body="Files and folders people share with you will appear here after you accept them." />;
  return <div>
    {offers.length > 0 && <section><h2 className="px-4 pt-4 pb-2 text-[13px] font-bold tracking-wide text-muted uppercase">Offers</h2><div className="conv-list bg-surface">{offers.map((offer) => <div key={offer.share.id} className="flex min-h-13 items-center gap-3 border-b border-sep px-4 py-3 last:border-b-0"><span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-surface-2 text-accent"><Share2 className="size-[18px]" aria-hidden="true" /></span><div className="min-w-0 flex-1"><div className="truncate font-semibold">{offer.name || "Shared item"}</div><div className="truncate text-sm text-muted">from {offer.share.issuer} · {offer.share.role}</div></div><Button size="sm" onClick={() => onAccept(offer)}>Accept</Button></div>)}</div></section>}
    {mounts.length > 0 && <section><h2 className="px-4 pt-4 pb-2 text-[13px] font-bold tracking-wide text-muted uppercase">Shared with you</h2><div className="conv-list bg-surface">{mounts.map((mount) => <button key={`${mount.drive}:${mount.node}`} className="flex min-h-13 w-full items-center gap-3 border-b border-sep px-4 py-3 text-left transition-colors last:border-b-0 [@media(hover:hover)]:hover:bg-surface-2" onClick={() => onOpen(mount)}><span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-surface-2 text-accent"><Folder className="size-[18px]" aria-hidden="true" /></span><span className="min-w-0 flex-1"><span className="block truncate font-semibold">{mount.name || "Shared item"}</span><span className="block truncate text-sm text-muted">{mount.drive} · {mount.role}</span></span></button>)}</div></section>}
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
  const [password, setPassword] = useState("");
  const [linkDays, setLinkDays] = useState("7");
  const [linkUrl, setLinkUrl] = useState("");
  const [requestUrl, setRequestUrl] = useState("");
  useEffect(() => { void sharesForFile(identity, file).then(setShares, () => {}); }, [identity, file]);
  return <form onSubmit={(event) => { event.preventDefault(); const member = field.current?.value.trim(); if (!member) return; setBusy(true); void shareBrowserFile(identity, file, member, role).then(({ notified }) => { toast(notified ? `Shared with ${member}` : `Access granted, but ${member} could not be notified`, notified ? "success" : "warning", 7000); close(); }, (cause) => { toast(errorMessage(cause), "error", 7000); setBusy(false); }); }}>
    <FormGroup><Label htmlFor="share-member">Poweur ID</Label><Input ref={field} id="share-member" autoComplete="off" placeholder="alex.example.com" /></FormGroup>
    <FormGroup><Label htmlFor="share-role">Access</Label><select id="share-role" value={role} onChange={(event) => setRole(event.currentTarget.value as ShareRole)} className="input w-full rounded-control border-[1.5px] border-sep bg-surface px-4 py-[13px]"><option value="read">Can view</option><option value="write">Can edit</option><option value="admin">Can manage</option></select></FormGroup>
    <Notice>The recipient gets a signed offer. The relay never receives this file's decryption key in plaintext.</Notice>
    <div className="my-5 border-t border-sep pt-4">
      <Label htmlFor="link-password">Share a link</Label>
      <p className="mb-3 text-sm text-muted">Anyone with the complete link can view this {file.manifest.kind}. Add a password for another layer of protection.</p>
      <div className="grid grid-cols-[1fr_auto] gap-2"><Input id="link-password" type="password" autoComplete="new-password" placeholder="Password (optional)" value={password} onChange={(event) => setPassword(event.currentTarget.value)} /><select aria-label="Link expiry" value={linkDays} onChange={(event) => setLinkDays(event.currentTarget.value)} className="input rounded-control border-[1.5px] border-sep bg-surface px-3"><option value="1">1 day</option><option value="7">7 days</option><option value="30">30 days</option><option value="">Never</option></select><Button type="button" variant="secondary" className="col-span-2" disabled={busy} onClick={() => { const expires = linkDays ? new Date(Date.now() + Number(linkDays) * 86_400_000).toISOString().replace(/\.\d{3}Z$/, "Z") : ""; setBusy(true); void linkBrowserFile(identity, file, password, expires).then(({ share, url }) => { setShares((current) => [...current, share]); setLinkUrl(url); setBusy(false); }, (cause) => { toast(errorMessage(cause), "error", 7000); setBusy(false); }); }}><Link2 className="size-4" />Create link</Button></div>
      {linkUrl && <div className="mt-2 flex gap-2"><Input aria-label="Share link" readOnly value={linkUrl} /><IconButton type="button" aria-label="Copy share link" onClick={() => { if (!navigator.clipboard) { toast("Copy is not available in this browser", "error"); return; } void navigator.clipboard.writeText(linkUrl).then(() => toast("Link copied", "success"), (cause) => toast(errorMessage(cause), "error")); }}><Copy className="size-4" /></IconButton></div>}
      {file.manifest.kind === "folder" && <div className="mt-4 border-t border-sep pt-4"><p className="mb-2 text-sm text-muted">Create an upload-only request. Visitors can add encrypted files but cannot see this folder or anyone else's submissions.</p><Button type="button" variant="secondary" className="w-full" disabled={busy} onClick={() => { const expires = linkDays ? new Date(Date.now() + Number(linkDays) * 86_400_000).toISOString().replace(/\.\d{3}Z$/, "Z") : ""; setBusy(true); void fileRequestBrowserLink(identity, file, password, expires).then(({ share, url }) => { setShares((current) => [...current, share]); setRequestUrl(url); setBusy(false); }, (cause) => { toast(errorMessage(cause), "error", 7000); setBusy(false); }); }}><Upload className="size-4" />Create file request</Button>{requestUrl && <div className="mt-2 flex gap-2"><Input aria-label="File request link" readOnly value={requestUrl} /><IconButton type="button" aria-label="Copy file request link" onClick={() => { if (!navigator.clipboard) { toast("Copy is not available in this browser", "error"); return; } void navigator.clipboard.writeText(requestUrl).then(() => toast("File request copied", "success"), (cause) => toast(errorMessage(cause), "error")); }}><Copy className="size-4" /></IconButton></div>}</div>}
    </div>
    {shares.length > 0 && <div className="mb-4"><Label>Access</Label><div className="overflow-hidden rounded-control bg-surface-2">{shares.map((share) => <div key={share.id} className="flex items-center gap-2 border-b border-sep px-3 py-2 last:border-0"><span className="min-w-0 flex-1 truncate text-sm">{share.member || (share.role === "create" ? "File request" : "Anyone with the link")} · {share.role}</span><Button type="button" variant="danger" size="sm" data-revoke-share={share.id} onClick={() => { setBusy(true); void revokeBrowserShare(identity, share).then(() => { setShares((current) => current.filter((entry) => entry.id !== share.id)); if (share.role === "create") setRequestUrl(""); else if (share.link) setLinkUrl(""); toast("Access revoked", "success"); setBusy(false); }, (cause) => { toast(errorMessage(cause), "error", 7000); setBusy(false); }); }}>Revoke</Button></div>)}</div></div>}
    <div className="flex gap-2"><Button variant="secondary" className="min-h-11 flex-1" onClick={close}>Cancel</Button><Button type="submit" className="min-h-11 flex-1 p-3 text-[15px]" disabled={busy}>{busy ? "Sharing…" : "Share"}</Button></div>
  </form>;
}
