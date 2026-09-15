/**
 * Which component draws each destination, sub-page and front door. T7–T11
 * replace the remaining NotPorted entries one by one; the shell never changes.
 */
import type { Destination, SubPageId } from "../state/route";
import { useSession } from "../state/session";
import { AddId } from "./AddId";
import { Claim } from "./Claim";
import { IdentityDoor } from "./doors/IdentityDoor";
import { Landing } from "./doors/Landing";
import { Welcome } from "./gates";
import { NotPortedDestination, NotPortedSubPage } from "./NotPorted";
import { Onboarding } from "./Onboarding";
import { SignInApproval } from "./SignInApproval";
import { Unlock } from "./Unlock";

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

export function SubScreen({ sub }: { sub: SubPageId }) {
  switch (sub) {
    case "unlock":
      return <Unlock />;
    case "onboarding":
      return <Onboarding />;
    case "claim":
      return <Claim />;
    case "auth":
      return <SignInApproval />;
    case "new-chat":
      return <NotPortedSubPage title="New chat" task="T7" />;
    case "thread":
      return <NotPortedSubPage title="Conversation" task="T7" />;
    default:
      return <AddId />;
  }
}

/** Which door, decided by the host; `unknown` keeps the generic welcome. */
export function FrontDoorScreen() {
  const mode = useSession((state) => state.mode.mode);
  switch (mode) {
    case "launcher":
    case "shell":
      return <Landing />;
    case "identity":
      return <IdentityDoor />;
    default:
      return <Welcome />;
  }
}
