/**
 * Which component draws each destination, sub-page and front door. T6–T11
 * replace the NotPorted entries one by one; the shell never changes.
 */
import type { Destination, SubPageId } from "../state/route";
import { useSession } from "../state/session";
import { Welcome } from "./gates";
import { NotPortedDestination, NotPortedDoor, NotPortedSubPage } from "./NotPorted";

const DESTINATION_TITLES: Record<Destination, { title: string; task: string }> = {
  messages: { title: "Messages", task: "T7" },
  contacts: { title: "Contacts", task: "T8" },
  files: { title: "Files", task: "T9" },
  launcher: { title: "Apps", task: "T10" },
  settings: { title: "Settings", task: "T11" },
};

export function DestinationScreen({ page }: { page: Destination }) {
  const { title, task } = DESTINATION_TITLES[page];
  return <NotPortedDestination title={title} task={task} />;
}

const SUB_TITLES: Record<SubPageId, { title: string; task: string }> = {
  "add-id": { title: "Add identity", task: "T6" },
  unlock: { title: "Unlock", task: "T6" },
  onboarding: { title: "Get started", task: "T6" },
  claim: { title: "Claim your name", task: "T10" },
  auth: { title: "Sign in", task: "T6" },
  "new-chat": { title: "New chat", task: "T7" },
  thread: { title: "Conversation", task: "T7" },
};

export function SubScreen({ sub }: { sub: SubPageId }) {
  const { title, task } = SUB_TITLES[sub] ?? SUB_TITLES["add-id"];
  return <NotPortedSubPage title={title} task={task} />;
}

/** Which door, decided by the host; `unknown` keeps the generic welcome. */
export function FrontDoorScreen() {
  const mode = useSession((state) => state.mode.mode);
  switch (mode) {
    case "launcher":
    case "shell":
    case "identity":
      return <NotPortedDoor mode={mode} task="T6" />;
    default:
      return <Welcome />;
  }
}
