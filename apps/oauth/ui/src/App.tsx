import type { Page } from "./lib/page";
import { Account } from "./pages/Account";
import { Approve } from "./pages/Approve";
import { ClientDetail, ClientNew, Developers } from "./pages/Developers";
import { Consent } from "./pages/Consent";
import { ErrorPage } from "./pages/ErrorPage";
import { Home } from "./pages/Home";
import { Identify } from "./pages/Identify";
import { Abuse, Privacy, Security } from "./pages/Policy";

/** The server chose the page; this only picks its component. */
export function App({ page }: { page: Page }) {
  switch (page.page) {
    case "home":
      return <Home page={page as never} />;
    case "identify":
      return <Identify page={page as never} />;
    case "await":
      return <Approve page={page as never} />;
    case "consent":
      return <Consent page={page as never} />;
    case "account":
      return <Account page={page as never} />;
    case "developers":
      return <Developers page={page as never} />;
    case "client_new":
      return <ClientNew page={page as never} />;
    case "client":
      return <ClientDetail page={page as never} />;
    case "privacy":
      return <Privacy page={page as never} />;
    case "security":
      return <Security page={page as never} />;
    case "abuse":
      return <Abuse page={page as never} />;
    default:
      return <ErrorPage page={page as never} />;
  }
}
