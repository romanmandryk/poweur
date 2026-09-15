/**
 * The public profile editor (E15-T5): display name, bio, avatar, one link.
 * Shared by onboarding (no Save button; the flow's Continue calls `save()`)
 * and Settings (E21-T11).
 */
import { useImperativeHandle, useRef, useState, type Ref } from "react";
import { saveProfile } from "../actions/account";
import { cn } from "../lib/cn";
import { handleOf } from "../lib/identity";
import { useSession } from "../state/session";
import { toast } from "../state/ui";
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
  const avatarFile = useRef<File | null>(null);
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<{ text: string; tone: "" | "ok" | "warn" }>({ text: "", tone: "" });
  const value = (id: string) => form.current?.querySelector<HTMLInputElement>(`#${id}`)?.value ?? "";

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
          avatarFile: avatarFile.current,
          avatarPath: doc.avatar ?? null,
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
    hasInput: () => Boolean(value("pe-name").trim() || value("pe-bio").trim() || avatarFile.current),
  }));

  return (
    <div ref={form} className="profile-editor">
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
        <Label htmlFor="pe-avatar">Avatar</Label>
        <p className="mb-1.5 text-[13px] text-muted">
          Stored in your own <code>/public</code> folder — a profile can never point at someone else's server.
        </p>
        <Input
          id="pe-avatar"
          type="file"
          accept="image/*"
          onChange={(event) => {
            avatarFile.current = event.currentTarget.files?.[0] ?? null;
          }}
        />
        {doc.avatar && (
          <p id="pe-avatar-current" className="text-[13px]">
            Current: {doc.avatar}
          </p>
        )}
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
