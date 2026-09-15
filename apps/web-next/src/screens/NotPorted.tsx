/**
 * Stand-in for a screen the rewrite has not reached yet (EPIC-021 T6–T11).
 * Says so plainly and links to the same screen in the legacy app, which runs
 * on this origin with the same identity. Deleted when the last task lands.
 */
import { useRoute } from "../state/route";
import { Button } from "../ui/Button";
import { EmptyState } from "../ui/Display";
import { DestHeader, SubPage } from "../ui/Layout";

/** `../app/` from `/newapp/`; meaningless in the shell, and temporary. */
const LEGACY_APP = "../app/";

function Body({ task }: { task: string }) {
  return (
    <EmptyState
      icon="🚧"
      title="Not ported yet"
      body={`This screen arrives with EPIC-021 ${task}. The current app has it, with the same identity.`}
      action={
        <a href={LEGACY_APP} className="btn btn-secondary inline-flex rounded-control bg-accent-soft px-5 py-3 font-semibold text-accent no-underline">
          Open current app
        </a>
      }
    />
  );
}

export function NotPortedDestination({ title, task }: { title: string; task: string }) {
  return (
    <>
      <DestHeader title={title} />
      <Body task={task} />
    </>
  );
}

export function NotPortedSubPage({ title, task }: { title: string; task: string }) {
  const pop = useRoute((state) => state.pop);
  return (
    <SubPage title={title} onBack={pop}>
      <Body task={task} />
    </SubPage>
  );
}

export function NotPortedDoor({ mode, task }: { mode: string; task: string }) {
  const push = useRoute((state) => state.push);
  return (
    <div className="flex min-h-full flex-col items-center justify-center p-6">
      <Body task={task} />
      <p className="text-[13px] text-muted">Front door: {mode}</p>
      <Button variant="link" className="mt-2" onClick={() => push("add-id")}>
        Add an identity instead
      </Button>
    </div>
  );
}
