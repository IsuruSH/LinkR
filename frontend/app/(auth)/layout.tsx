import { Link2 } from "lucide-react";

/**
 * Server component. Static chrome around the auth forms — no state, no hooks,
 * so it never ships to the browser.
 */
export default function AuthLayout({ children }: { children: React.ReactNode }) {
  return (
    <main className="flex flex-1 items-center justify-center px-4 py-12">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-2 text-center">
          <div className="bg-primary text-primary-foreground flex size-11 items-center justify-center rounded-xl">
            <Link2 className="size-5" aria-hidden />
          </div>
          <h1 className="text-2xl font-semibold tracking-tight">Linkr</h1>
          <p className="text-muted-foreground text-sm">Short links with click analytics.</p>
        </div>
        {children}
      </div>
    </main>
  );
}
