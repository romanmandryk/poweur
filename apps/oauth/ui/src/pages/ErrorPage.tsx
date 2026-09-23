import { CircleAlert } from "lucide-react";
import type { ErrorData, Page } from "../lib/page";
import { LinkButton } from "../ui/Button";
import { Card } from "../ui/Display";
import { Shell } from "../ui/Shell";

export function ErrorPage({ page }: { page: Page<ErrorData> }) {
  return (
    <Shell page={page} narrow>
      <Card className="animate-fade-in-up text-center">
        <span className="mx-auto mb-4 flex size-14 items-center justify-center rounded-full bg-danger/12 text-danger">
          <CircleAlert className="size-7" aria-hidden="true" />
        </span>
        <h1 className="text-[22px] font-extrabold">{page.title}</h1>
        <p id="error-message" className="mt-2 text-[15px] text-muted">
          {page.data?.message}
        </p>
        <LinkButton href="/" variant="secondary" className="mt-6">
          Home
        </LinkButton>
      </Card>
    </Shell>
  );
}
