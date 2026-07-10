"use client";

import { LogOut } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";

/**
 * Signing out is a POST to a route handler, not a link.
 *
 * The cookie is httpOnly, so the browser cannot clear it; only the server can.
 * And it must be a POST: a GET that mutates session state can be triggered by
 * any <img src="/api/auth/logout"> on a page the user visits.
 */
export function SignOutButton() {
  const router = useRouter();
  const [pending, setPending] = useState(false);

  async function signOut() {
    setPending(true);
    try {
      const res = await fetch("/api/auth/logout", { method: "POST" });
      if (!res.ok) throw new Error("logout failed");

      router.push("/login");
      // Drop any cached server-rendered output for the authenticated routes.
      router.refresh();
    } catch {
      toast.error("Could not sign out", { description: "Please try again." });
      setPending(false);
    }
  }

  return (
    <Button variant="ghost" size="sm" onClick={() => void signOut()} disabled={pending}>
      <LogOut className="size-4" aria-hidden />
      <span className="hidden sm:inline">{pending ? "Signing out…" : "Sign out"}</span>
    </Button>
  );
}
