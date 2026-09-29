/**
 * The shell (E21-T4). Same decision tree as the legacy `render()` — gate
 * sub-page, else front door, else header + destination + detail + nav — but a
 * state change now re-renders only what reads it: the header and nav are the
 * same DOM nodes for the life of the session, which is what ends the flicker.
 */
import { useLayoutEffect, type ReactNode } from "react";
import { cn } from "../lib/cn";
import { DETAIL_SUBS, useRoute } from "../state/route";
import { useSession } from "../state/session";
import { Locked, Welcome } from "../screens/gates";
import { DestinationScreen, FrontDoorScreen, SubScreen } from "../screens/registry";
import { BottomNav } from "./BottomNav";
import { Header } from "./Header";
import { LoadingOverlay, PanelHost, Toaster } from "./Overlays";
import { useBackNavigation } from "./useBackNavigation";
import { useMessaging } from "./useMessaging";
import { useFilesSync } from "./useFilesSync";
import { usePendingChat } from "./usePendingChat";
import { isPublicAnonymousRoute, PublicAnonymousComposer } from "../screens/PublicAnonymous";

export function App() {
  if (isPublicAnonymousRoute()) return <PublicAnonymousComposer />;
  return <PrivateApp />;
}

function PrivateApp() {
  const identity = useSession((state) => state.identity);
  const hostMode = useSession((state) => state.mode.mode);
  const hostProbed = useSession((state) => state.mode.probed);
  const page = useRoute((state) => state.page);
  const sub = useRoute((state) => state.sub);
  useBackNavigation();
  useMessaging();
  useFilesSync();
  usePendingChat();

  useLayoutEffect(() => {
    document.getElementById("page-content")?.scrollTo?.({ top: 0 });
  }, [page, sub]);

  const detail = sub !== null && DETAIL_SUBS.has(sub);
  const gate = sub !== null && !detail;
  // No identity on this device: the destinations are not reachable, so the
  // door is the whole page (E15-T7). A launcher host is that door even when
  // this origin still has an identity — the claim hand-off leaves one behind,
  // and the public page must not offer to open it. Same while the host is
  // still unknown: guessing from storage paints the wrong person.
  const launcherDoor = hostMode === "launcher";
  const doorUndecided = Boolean(identity) && hostMode === "unknown" && hostProbed === false;
  const frontDoor = sub === null && (!identity || launcherDoor || doorUndecided);
  const shell = !gate && !frontDoor;

  let content: ReactNode;
  if (gate) {
    content = <SubScreen sub={sub} />;
  } else if (frontDoor) {
    content = <FrontDoorScreen />;
  } else {
    content = (
      <>
        <Header className={cn("md:col-start-2 md:row-start-1", detail && "lg:col-span-2")} />
        <main
          id="page-content"
          className={cn(
            "page-content flex-1 overflow-x-hidden overflow-y-auto overscroll-contain",
            "md:col-start-2 md:row-start-2 md:mx-auto md:w-full md:max-w-[720px] md:pb-6",
            detail && "lg:mx-0 lg:max-w-none",
          )}
        >
          <DestinationOutlet />
        </main>
        {detail && (
          <div
            id="detail-pane"
            className="detail-pane fixed inset-0 z-200 overflow-y-auto bg-bg lg:static lg:col-start-3 lg:row-start-2 lg:border-l lg:border-sep lg:bg-surface"
          >
            <SubScreen sub={sub} />
          </div>
        )}
        <BottomNav className="md:col-start-1 md:row-span-2 md:row-start-1" />
      </>
    );
  }

  return (
    <>
      <div
        id="app"
        className={cn(
          "relative mx-auto flex h-dvh max-w-[480px] flex-col overflow-hidden bg-bg",
          shell && "md:grid md:max-w-none md:grid-cols-[80px_minmax(0,1fr)] md:grid-rows-[auto_minmax(0,1fr)] lg:grid-cols-[240px_minmax(0,1fr)]",
          shell && detail && "lg:grid-cols-[240px_minmax(0,1fr)_minmax(0,420px)]",
        )}
      >
        {content}
      </div>
      <Toaster />
      <LoadingOverlay />
      <PanelHost />
    </>
  );
}

/** Every destination but the launcher needs an identity; most need keys too. */
function DestinationOutlet() {
  const page = useRoute((state) => state.page);
  const identity = useSession((state) => state.identity);
  const unlocked = useSession((state) => state.unlocked);

  if (!identity && page !== "launcher") return <Welcome />;
  if (!unlocked && page !== "launcher" && page !== "settings") return <Locked />;
  return <DestinationScreen page={page} />;
}
