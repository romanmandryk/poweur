/**
 * The Apps destination. It used to *be* the create-identity form; claiming
 * moved to the front door (E15-T7), and what is left is the seam EPIC-010
 * fills — said plainly rather than filled with something else.
 */
import { EmptyState } from "../ui/Display";
import { DestHeader } from "../ui/Layout";

export function Launcher() {
  return (
    <>
      <DestHeader title="Apps" />
      <EmptyState icon="🚀" title="No apps yet" body="Apps and automations that read and write your Poweur files will appear here." />
    </>
  );
}
