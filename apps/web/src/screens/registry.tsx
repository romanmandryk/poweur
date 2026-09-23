/** Which component draws each destination, sub-page and front door. */
import type { Destination, SubPageId } from "../state/route";
import { useSession } from "../state/session";
import { AddId } from "./AddId";
import { Claim } from "./Claim";
import { Contacts } from "./contacts/Contacts";
import { IdentityDoor } from "./doors/IdentityDoor";
import { Landing } from "./doors/Landing";
import { Files } from "./files/Files";
import { Welcome } from "./gates";
import { Launcher } from "./Launcher";
import { Messages } from "./messages/Messages";
import { NewChat } from "./messages/NewChat";
import { ThreadScreen } from "./messages/Thread";
import { Onboarding } from "./Onboarding";
import { PairDevice } from "./PairDevice";
import { Settings } from "./settings/Settings";
import { SignInApproval } from "./SignInApproval";
import { Unlock } from "./Unlock";

export function DestinationScreen({ page }: { page: Destination }) {
  switch (page) {
    case "contacts":
      return <Contacts />;
    case "files":
      return <Files />;
    case "launcher":
      return <Launcher />;
    case "settings":
      return <Settings />;
    default:
      return <Messages />;
  }
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
    case "pair":
      return <PairDevice />;
    case "new-chat":
      return <NewChat />;
    case "thread":
      return <ThreadScreen />;
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
