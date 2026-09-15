/**
 * "New message" only asks who (E15-T13). Writing happens in the conversation
 * itself, like every chat app.
 */
import { useRef, useState } from "react";
import { ArrowRight } from "lucide-react";
import { openNewChat } from "../../actions/messages";
import { resolveForActive } from "../../actions/relay";
import { IdentityInput, type IdentityInputHandle } from "../../components/IdentityInput";
import { domainOf } from "../../lib/identity";
import { useData } from "../../state/data";
import { useRoute } from "../../state/route";
import { useSession } from "../../state/session";
import { toast } from "../../state/ui";
import { Button } from "../../ui/Button";
import { SubPage } from "../../ui/Layout";

export function NewChat() {
  const identity = useSession((state) => state.identity) ?? "";
  const contacts = useData((state) => state.contacts.list);
  const pop = useRoute((state) => state.pop);
  const input = useRef<IdentityInputHandle>(null);
  const [opening, setOpening] = useState(false);
  const [status, setStatus] = useState("");

  const open = async (picked?: string) => {
    let target = picked;
    if (!target) {
      const result = await input.current?.lookup();
      if (!result) {
        toast("Enter a Poweur ID we can find", "warning");
        return;
      }
      target = result.identity;
    }
    setOpening(true);
    setStatus("Opening…");
    if (!(await openNewChat(target))) {
      setOpening(false);
      setStatus("");
    }
  };

  return (
    <SubPage
      title="New message"
      onBack={pop}
      className="new-chat"
      bodyClassName="new-chat-body flex flex-col gap-3"
      footer={
        <Button id="btn-open-chat" disabled={opening} onClick={() => void open()}>
          Open chat <ArrowRight className="size-5" aria-hidden="true" />
        </Button>
      }
    >
      <IdentityInput
        ref={input}
        resolve={resolveForActive}
        contacts={contacts}
        defaultDomain={domainOf(identity)}
        label="To"
        onSubmit={(picked) => void open(picked)}
      />
      <p id="new-chat-status" className="compose-status min-h-5 text-sm text-muted">
        {status}
      </p>
    </SubPage>
  );
}
