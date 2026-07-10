import { CreateLinkDialog } from "@/components/features/create-link-dialog";
import { LinksTable } from "@/components/features/links-table";

export const metadata = { title: "Links · Linkr" };

/**
 * Server component. It renders the page frame and two client islands: the create
 * dialog (form state) and the table (TanStack Query, clipboard, dropdowns).
 *
 * The links are not fetched here and passed down. They are fetched in the client
 * by TanStack Query, because the same cache has to serve the create and delete
 * mutations' invalidations. Server-fetching them would mean the list after a
 * mutation comes from a different source than the list on first paint.
 */
export default function LinksPage() {
  return (
    <div className="space-y-6">
      <div className="flex items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Your links</h1>
          <p className="text-muted-foreground mt-1 text-sm">
            Every short link you own, and how often each has been clicked.
          </p>
        </div>
        <CreateLinkDialog />
      </div>

      <LinksTable />
    </div>
  );
}
