/**
 * The Files destination (E15-T4): our own tree, or what someone shared with
 * us — the relay's grant engine decides what a visitor sees, so an owner who
 * shared nothing simply looks empty.
 */
import { useEffect } from "react";
import { FileText, Folder, FolderOpen, FolderPlus, Link2, Lock, Pencil, Share2, Trash2, Upload } from "lucide-react";
import { formatBytes, ROOT_INFO } from "@poweur/client";
import { loadContacts } from "../../actions/contacts";
import {
  deleteEntry,
  downloadEntry,
  grantsForPath,
  isShareablePath,
  loadFiles,
  loadMounts,
  newFolder,
  openOwnerTree,
  renameEntry,
  setFilesOwner,
  storageFullMessage,
  uploadFiles,
  watchChanges,
} from "../../actions/files";
import { resolveForActive } from "../../actions/relay";
import { IdentityInput } from "../../components/IdentityInput";
import { onActivateKeys } from "../../lib/a11y";
import { handleOf } from "../../lib/identity";
import { useData } from "../../state/data";
import { useSession } from "../../state/session";
import { Avatar } from "../../ui/Avatar";
import { Button } from "../../ui/Button";
import { Chip, EmptyState, SectionLabel } from "../../ui/Display";
import { DestHeader } from "../../ui/Layout";
import { Tab, TabBar } from "../../ui/Tabs";
import { usePeerAvatars } from "../../actions/avatars";
import { openSharePanel, openSharesPanel } from "./SharePanel";

export function Files() {
  const files = useData((state) => state.files);
  const unlocked = useSession((state) => state.unlocked);
  const visiting = Boolean(files.owner);
  const picking = files.picking && !visiting;
  const atRoot = !files.path;

  // First visit to a tree loads it; the owner picker and the share dialog
  // both need contacts.
  useEffect(() => {
    if (!unlocked) return;
    if (!files.loaded && !files.picking) void loadFiles(files.path);
    void loadContacts();
    void loadMounts();
  }, [unlocked, files.loaded, files.owner, files.picking]);

  // The changes feed runs only while Files is on screen, per open tree.
  useEffect(() => {
    if (!unlocked || picking) return;
    return watchChanges();
  }, [unlocked, files.owner, picking]);

  return (
    <>
      <DestHeader title="Files">
        {atRoot && !visiting && (
          <Button id="btn-shares" size="sm" variant="secondary" title="Shares you have made" aria-label="Shares you have made" onClick={openSharesPanel}>
            <Link2 className="size-4" aria-hidden="true" />
          </Button>
        )}
        {!atRoot && !visiting && (
          <Button id="btn-new-folder" size="sm" variant="secondary" title="New folder" aria-label="New folder" onClick={() => void newFolder()}>
            <FolderPlus className="size-4" aria-hidden="true" />
          </Button>
        )}
        {!atRoot && (
          <label
            htmlFor="ff-upload"
            title="Upload"
            aria-label="Upload"
            className="btn btn-sm inline-flex min-h-10 cursor-pointer items-center justify-center rounded-control bg-surface-2 px-4 py-2 text-sm font-semibold text-fg"
          >
            <Upload className="size-4" aria-hidden="true" />
            <input
              id="ff-upload"
              type="file"
              multiple
              hidden
              onChange={(event) => {
                const picked = Array.from(event.currentTarget.files ?? []);
                event.currentTarget.value = "";
                void uploadFiles(picked);
              }}
            />
          </label>
        )}
      </DestHeader>

      <TabBar className="dest-toolbar file-sources mb-3" aria-label="Which files">
        <Tab
          id="btn-files-mine"
          active={!visiting && !picking}
          onClick={() => {
            if (!files.owner && !files.picking) return;
            setFilesOwner(null);
          }}
        >
          My files
        </Tab>
        <Tab
          id="btn-files-shared"
          active={visiting || picking}
          onClick={() => {
            if (files.owner) return;
            useData.setState((state) => ({ files: { ...state.files, picking: true } }));
          }}
        >
          Shared with me
        </Tab>
      </TabBar>

      {picking ? (
        <OwnerPicker />
      ) : (
        <>
          {visiting && (
            <div className="visitor-banner border-b border-sep bg-accent-soft px-4 py-2.5 text-[13px] leading-[1.4] text-fg">
              Browsing <strong>{files.owner}</strong>. You see only what they granted you; writing works where they allowed it.
              <Button id="btn-leave-owner" variant="link" className="ml-1.5" onClick={() => setFilesOwner(null, { picking: true })}>
                Leave
              </Button>
            </div>
          )}
          {files.quota && !visiting && <QuotaBar quota={files.quota} />}
          <Breadcrumbs path={files.path} />
          {!(files.loading && files.entries.length === 0) && <Listing />}
        </>
      )}
    </>
  );
}

/**
 * How much is used. The limit itself is not shown: free storage is small and
 * not advertised, so the bar only speaks up when it is nearly reached.
 */
function QuotaBar({ quota }: { quota: any }) {
  const ratio = quota.quota_bytes > 0 ? quota.used_bytes / quota.quota_bytes : 0;
  return (
    <div className="quota-bar px-4 pb-3">
      <div className="quota-line flex justify-between text-[13px] text-muted">
        <span>{formatBytes(quota.used_bytes)} used</span>
        <span>{quota.provider ?? ""}</span>
      </div>
      {ratio >= 0.9 && (
        <p id="quota-warning" role="status" className="quota-warning mt-2 rounded-control bg-warning/12 px-3 py-2 text-[13px] text-fg">
          {storageFullMessage(quota.contact, ratio >= 1)}
        </p>
      )}
    </div>
  );
}

function Breadcrumbs({ path }: { path: string }) {
  const crumbs = path ? path.split("/") : [];
  return (
    <nav className="breadcrumbs flex flex-wrap items-center gap-1 px-4 pb-2.5 text-[13px]" aria-label="Folder path">
      <Button variant="link" data-nav-path="" className="font-semibold" onClick={() => void loadFiles("")}>
        home
      </Button>
      {crumbs.map((crumb, index) => {
        const target = crumbs.slice(0, index + 1).join("/");
        return (
          <span key={target} className="flex items-center gap-1">
            <span className="text-muted">/</span>
            <Button variant="link" data-nav-path={target} onClick={() => void loadFiles(target)}>
              {crumb}
            </Button>
          </span>
        );
      })}
    </nav>
  );
}

function Listing() {
  const files = useData((state) => state.files);
  const visiting = Boolean(files.owner);
  const atRoot = !files.path;

  if (files.entries.length === 0) {
    if (visiting) {
      return (
        <EmptyState
          icon={Lock}
          title="Nothing shared here"
          body={`${files.owner} has not granted you anything under this folder, or the grant was revoked.`}
        />
      );
    }
    return atRoot ? (
      <EmptyState icon={FolderOpen} title="No roots yet" body="Your storage roots appear once the relay provisions them." />
    ) : (
      <EmptyState icon={FolderOpen} title="Empty folder" body="Upload a file or create a folder to get started." />
    );
  }

  return (
    <div className="conv-list bg-surface">
      {files.entries.map((entry: any) => {
        const rootInfo = atRoot ? (ROOT_INFO as Record<string, { badge: string; desc: string }>)[entry.name] : null;
        const shared = !visiting && grantsForPath(files.grants, entry.path).length > 0;
        const open = () => (entry.dir ? void loadFiles(entry.path) : void downloadEntry(entry.path));
        return (
          <div key={entry.path} className="conv-row file-row flex min-h-13 items-center gap-3 border-b border-sep px-4 py-3 last:border-b-0 [@media(hover:hover)]:hover:bg-surface-2">
            <div className="file-icon flex w-9 shrink-0 justify-center">
              {entry.dir ? (
                <Folder className="size-[22px] text-accent" strokeWidth={1.8} aria-hidden="true" />
              ) : (
                <FileText className="size-[22px] text-muted" strokeWidth={1.8} aria-hidden="true" />
              )}
            </div>
            <div
              role="button"
              tabIndex={0}
              {...(entry.dir ? { "data-open-dir": entry.path } : { "data-download": entry.path })}
              onClick={open}
              onKeyDown={onActivateKeys(open)}
              className="conv-info min-w-0 flex-1 cursor-pointer"
            >
              <div className="conv-name flex flex-wrap items-center gap-1.5 text-base font-semibold">
                {entry.name}
                {rootInfo && <Chip>{rootInfo.badge}</Chip>}
                {shared && <Chip tone="accent">Shared</Chip>}
              </div>
              <div className="conv-preview mt-px truncate text-sm text-muted">
                {rootInfo ? rootInfo.desc : entry.dir ? "folder" : formatBytes(entry.size)}
              </div>
            </div>
            {!atRoot && !visiting && (
              <div className="flex shrink-0 gap-1">
                {isShareablePath(entry.path) && (
                  <Button size="sm" variant="secondary" className="px-3" data-share={entry.path} aria-label={`Share ${entry.name}`} onClick={() => openSharePanel(entry.path)}>
                    <Share2 className="size-4" aria-hidden="true" />
                  </Button>
                )}
                <Button size="sm" variant="secondary" className="px-3" data-rename={entry.path} aria-label={`Rename ${entry.name}`} onClick={() => void renameEntry(entry.path)}>
                  <Pencil className="size-4" aria-hidden="true" />
                </Button>
                <Button size="sm" variant="secondary" className="px-3 text-danger" data-delete={entry.path} aria-label={`Delete ${entry.name}`} onClick={() => void deleteEntry(entry.path)}>
                  <Trash2 className="size-4" aria-hidden="true" />
                </Button>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

/**
 * Whose shared files to open. Grants live in the *owner's* tree, so the
 * visitor names the owner, exactly as the CLI does.
 */
function OwnerPicker() {
  const contacts = useData((state) => state.contacts.list);
  const mounts = useData((state) => state.files.mounts);
  const accepted = contacts.filter((contact) => contact.state === "accepted");
  usePeerAvatars(accepted.map((contact) => contact.identity));
  return (
    <div className="owner-picker px-4 pb-4">
      <p className="mb-2 text-[13px] text-muted">Open someone's tree to see what they have shared with you.</p>
      <IdentityInput resolve={resolveForActive} contacts={contacts} label="Whose files?" preview={false} onSubmit={openOwnerTree} />
      {mounts.length > 0 && (
        <>
          <SectionLabel className="px-0">Accepted shares</SectionLabel>
          <div className="conv-list overflow-hidden rounded-card bg-surface">
            {mounts.map((entry: any) => {
              const open = () => openOwnerTree(entry.mount.owner);
              return (
                <div
                  key={entry.mount.share_id}
                  role="button"
                  tabIndex={0}
                  data-open-share={entry.mount.share_id}
                  onClick={open}
                  onKeyDown={onActivateKeys(open)}
                  className="conv-row flex min-h-13 cursor-pointer items-center gap-3 border-b border-sep px-4 py-3 last:border-b-0 [@media(hover:hover)]:hover:bg-surface-2"
                >
                  <FolderOpen className="size-5 shrink-0 text-accent" aria-hidden="true" />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-base font-semibold">/{entry.mount.source_path}</div>
                    <div className="truncate text-sm text-muted">{entry.mount.owner}</div>
                  </div>
                  <Chip tone={entry.mount.permissions.includes("write") ? "warning" : "accent"}>
                    {entry.mount.permissions.includes("write") ? "read + write" : "read"}
                  </Chip>
                </div>
              );
            })}
          </div>
        </>
      )}
      {accepted.length > 0 ? (
        <>
          <SectionLabel className="px-0">Contacts</SectionLabel>
          <div className="conv-list overflow-hidden rounded-card bg-surface">
            {accepted.map((contact) => {
              const open = () => openOwnerTree(contact.identity);
              return (
                <div
                  key={contact.identity}
                  role="button"
                  tabIndex={0}
                  data-open-owner={contact.identity}
                  onClick={open}
                  onKeyDown={onActivateKeys(open)}
                  className="conv-row flex min-h-13 cursor-pointer items-center gap-3 border-b border-sep px-4 py-3 last:border-b-0 [@media(hover:hover)]:hover:bg-surface-2"
                >
                  <Avatar identity={contact.identity} size="md" />
                  <div className="conv-info min-w-0 flex-1">
                    <div className="conv-name text-base font-semibold">{contact.petname || handleOf(contact.identity)}</div>
                    <div className="conv-preview truncate text-sm text-muted">{contact.identity}</div>
                  </div>
                </div>
              );
            })}
          </div>
        </>
      ) : (
        <p className="text-[13px] text-muted">No contacts yet — type an identity above.</p>
      )}
    </div>
  );
}
