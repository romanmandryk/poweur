/**
 * The public profile editor (E15-T5): display name, bio, photo, one link.
 * Shared by onboarding (no Save button; the flow's Continue calls `save()`)
 * and Settings (E21-T11). A picked photo is cropped to a square straight away
 * and previewed in the same circle the rest of the app draws; it is uploaded
 * only when the profile is saved.
 */
import { useEffect, useImperativeHandle, useRef, useState, type Ref } from "react";
import { ImagePlus } from "lucide-react";
import { saveProfile } from "../actions/account";
import { blobToDataUrl, isWebImageType, MAX_AVATAR_BYTES, squareAvatar } from "../lib/avatar-image";
import { cn } from "../lib/cn";
import { handleOf } from "../lib/identity";
import { useAvatarSrc } from "../state/avatars";
import { useSession } from "../state/session";
import { toast } from "../state/ui";
import { Avatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { FormGroup, Input, Label, Textarea } from "../ui/Field";

export interface ProfileDocument {
  display_name?: string;
  bio?: string;
  avatar?: string;
  links?: { label?: string; url?: string }[];
}

export interface ProfileEditorHandle {
  save(): Promise<boolean>;
  /** True when anything was typed or picked — nothing typed is nothing to write. */
  hasInput(): boolean;
}

async function previewUrl(blob: Blob): Promise<string> {
  return typeof URL.createObjectURL === "function" ? URL.createObjectURL(blob) : blobToDataUrl(blob);
}

function releaseUrl(url: string | null) {
  if (url?.startsWith("blob:")) URL.revokeObjectURL?.(url);
}

export function ProfileEditor({
  ref,
  profile,
  onSaved,
  saveLabel = "Save profile",
  showSave = true,
}: {
  ref?: Ref<ProfileEditorHandle>;
  profile: ProfileDocument | null;
  onSaved?: (saved: ProfileDocument) => void;
  saveLabel?: string;
  showSave?: boolean;
}) {
  const identity = useSession((state) => state.identity) ?? "";
  const unlocked = useSession((state) => state.unlocked);
  const [doc] = useState<ProfileDocument>(() => profile ?? {});
  const form = useRef<HTMLDivElement>(null);
  const stored = useAvatarSrc(identity);
  const [picked, setPicked] = useState<{ blob: Blob; url: string } | null>(null);
  const [removed, setRemoved] = useState(false);
  const pickedUrl = useRef<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<{ text: string; tone: "" | "ok" | "warn" }>({ text: "", tone: "" });
  const value = (id: string) => form.current?.querySelector<HTMLInputElement>(`#${id}`)?.value ?? "";

  useEffect(() => () => releaseUrl(pickedUrl.current), []);

  const preview = picked?.url ?? (removed ? null : stored);
  const hasPhoto = Boolean(picked) || (!removed && Boolean(doc.avatar || stored));

  async function pick(file: File | null | undefined) {
    if (!file) return;
    if (!file.type.startsWith("image/")) {
      setStatus({ text: "Choose an image file for your photo", tone: "warn" });
      return;
    }
    if (file.size > MAX_AVATAR_BYTES) {
      setStatus({ text: "That photo is too large (15 MB at most)", tone: "warn" });
      return;
    }
    const blob = await squareAvatar(file);
    // Kept as picked only when other browsers can show it from our public URL.
    if (blob === file && !isWebImageType(file.type)) {
      setStatus({ text: "Use a JPEG, PNG, WebP or GIF photo", tone: "warn" });
      return;
    }
    const url = await previewUrl(blob);
    releaseUrl(pickedUrl.current);
    pickedUrl.current = url;
    setPicked({ blob, url });
    setRemoved(false);
    setStatus({ text: "", tone: "" });
  }

  function removePhoto() {
    releaseUrl(pickedUrl.current);
    pickedUrl.current = null;
    setPicked(null);
    setRemoved(true);
  }

  async function submit(): Promise<boolean> {
    if (!unlocked) {
      toast("Unlock your identity first", "warning");
      return false;
    }
    setSaving(true);
    setStatus({ text: "", tone: "" });
    try {
      const saved = await saveProfile(
        {
          displayName: value("pe-name"),
          bio: value("pe-bio"),
          avatarFile: picked?.blob ?? null,
          avatarPath: removed ? null : (doc.avatar ?? null),
          linkLabel: value("pe-link-label"),
          linkUrl: value("pe-link-url"),
        },
        (text) => setStatus({ text, tone: "" }),
      );
      setStatus({ text: "Saved", tone: "ok" });
      onSaved?.(saved);
      return true;
    } catch (error) {
      setStatus({ text: (error as Error).message, tone: "warn" });
      return false;
    } finally {
      setSaving(false);
    }
  }

  useImperativeHandle(ref, () => ({
    save: submit,
    hasInput: () => Boolean(value("pe-name").trim() || value("pe-bio").trim() || picked || removed),
  }));

  return (
    <div ref={form} className="profile-editor">
      <FormGroup className="mb-3.5">
        <Label htmlFor="pe-avatar">Photo</Label>
        <div className="pe-avatar-row flex items-center gap-4">
          <Avatar identity={identity} size="xl" src={preview} className="pe-avatar-preview" />
          <div className="flex min-w-0 flex-col items-start gap-1">
            <label
              htmlFor="pe-avatar"
              className="btn btn-sm inline-flex min-h-10 cursor-pointer items-center gap-2 rounded-control bg-surface-2 px-4 py-2 text-sm font-semibold text-fg active:opacity-80"
            >
              <ImagePlus className="size-4" aria-hidden="true" />
              {hasPhoto ? "Change photo" : "Choose photo"}
            </label>
            <input
              id="pe-avatar"
              type="file"
              accept="image/*"
              className="sr-only"
              onChange={(event) => {
                const file = event.currentTarget.files?.[0];
                // Picking the same file again after a remove should still work.
                event.currentTarget.value = "";
                void pick(file);
              }}
            />
            {hasPhoto && (
              <Button id="pe-avatar-remove" variant="link" className="text-danger" onClick={removePhoto}>
                Remove photo
              </Button>
            )}
          </div>
        </div>
        <p className="mt-2 text-[13px] text-muted">
          {picked
            ? "Cropped to fit the circle. It is uploaded with your profile."
            : removed
              ? "Your photo is removed when you save."
              : "Public: anyone who looks you up sees it. Stored in your own /public folder."}
        </p>
      </FormGroup>
      <FormGroup className="mb-3.5">
        <Label htmlFor="pe-name">Display name</Label>
        <Input id="pe-name" type="text" maxLength={256} defaultValue={doc.display_name ?? ""} placeholder={handleOf(identity)} />
      </FormGroup>
      <FormGroup className="mb-3.5">
        <Label htmlFor="pe-bio">Bio</Label>
        <Textarea
          id="pe-bio"
          className="pe-bio min-h-22 resize-y"
          maxLength={4096}
          defaultValue={doc.bio ?? ""}
          placeholder="A line or two about you"
        />
      </FormGroup>
      <FormGroup className="mb-3.5">
        <Label htmlFor="pe-link-url">Link</Label>
        <Input id="pe-link-label" type="text" placeholder="Label (optional)" defaultValue={doc.links?.[0]?.label ?? ""} />
        <Input id="pe-link-url" type="url" className="mt-2" placeholder="https://example.org" defaultValue={doc.links?.[0]?.url ?? ""} />
      </FormGroup>
      {showSave && (
        <Button id="pe-save" className="mt-4" disabled={saving} onClick={() => void submit()}>
          {saveLabel}
        </Button>
      )}
      <p
        id="pe-status"
        role="status"
        aria-live="polite"
        className={cn(
          "idin-status min-h-[18px] text-[13px] text-muted",
          status.tone === "ok" && "val-ok text-success",
          status.tone === "warn" && "val-warn text-warning",
        )}
      >
        {status.text}
      </p>
    </div>
  );
}
