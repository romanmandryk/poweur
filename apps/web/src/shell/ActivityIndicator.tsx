/**
 * One quiet "something is loading" for the whole shell. Screens used to print
 * their own "Loading…" line above the list, and the list jumped when it went
 * away; this floats under the header and takes no layout space. It waits a
 * moment before showing, so a fast read never blinks it.
 */
import { useEffect, useState } from "react";
import { LoaderCircle } from "lucide-react";
import { useData, type DataFields } from "../state/data";
import { useRoute } from "../state/route";
import { useUi } from "../state/ui";

const SHOW_AFTER_MS = 250;

type BusyData = Pick<DataFields, "tray" | "history" | "requests" | "anon" | "contacts" | "files">;

/**
 * Whether the screen on show is waiting for data it does not have yet. The
 * inbox and request queues drain in the background on every push; those stay
 * silent once a screen has something to show.
 */
export function destinationBusy(page: string, data: BusyData): boolean {
  switch (page) {
    case "messages":
      return (
        (data.history.loading && !data.history.loaded) ||
        (data.tray === "requests" && data.requests.loading && !data.requests.loaded) ||
        (data.tray === "anonymous" && data.anon.loading && !data.anon.loaded)
      );
    case "contacts":
      return data.contacts.loading;
    case "files":
      return data.files.loading;
    default:
      return false;
  }
}

export function ActivityIndicator() {
  const page = useRoute((state) => state.page);
  const busy = useData((state) => destinationBusy(page, state));
  // A pull-to-refresh draws its own spinner where the finger is.
  const refreshing = useUi((state) => state.refreshing);
  const wanted = busy && !refreshing;
  const [shown, setShown] = useState(false);

  useEffect(() => {
    if (!wanted) {
      setShown(false);
      return;
    }
    const timer = setTimeout(() => setShown(true), SHOW_AFTER_MS);
    return () => clearTimeout(timer);
  }, [wanted]);

  if (!shown) return null;
  return (
    <div
      role="status"
      aria-label="Loading"
      className="activity-indicator pointer-events-none absolute top-full left-1/2 z-10 mt-2 flex size-9 -translate-x-1/2 animate-fade-in items-center justify-center rounded-full bg-surface shadow-pop"
    >
      <LoaderCircle className="size-5 animate-spin text-accent" aria-hidden="true" />
    </div>
  );
}
