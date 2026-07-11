"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2 } from "lucide-react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { ApiError, fieldErrorFrom, toApiError } from "@/lib/api-error";
import { credentialsSchema, type CredentialsInput } from "@/lib/schemas";

type Mode = "login" | "register";

const COPY: Record<Mode, { submit: string; pending: string; alt: string; altHref: string; altLabel: string }> = {
  login: {
    submit: "Sign in",
    pending: "Signing in…",
    alt: "Don't have an account?",
    altHref: "/register",
    altLabel: "Create one",
  },
  register: {
    submit: "Create account",
    pending: "Creating account…",
    alt: "Already have an account?",
    altHref: "/login",
    altLabel: "Sign in",
  },
};

/**
 * Client component: it owns form state, focus, and submission. There is nothing
 * here a server component could do.
 *
 * It posts to /api/auth/{mode} — a Next route handler, not the Go API — because
 * only the server may see the JWT and turn it into an httpOnly cookie.
 */
export function AuthForm({ mode }: { mode: Mode }) {
  const router = useRouter();
  const searchParams = useSearchParams();
  const [formError, setFormError] = useState<string | null>(null);

  const form = useForm<CredentialsInput>({
    resolver: zodResolver(credentialsSchema),
    defaultValues: { email: "", password: "" },
    mode: "onSubmit",
  });

  const copy = COPY[mode];
  const isSubmitting = form.formState.isSubmitting;

  async function onSubmit(values: CredentialsInput) {
    setFormError(null);

    try {
      const res = await fetch(`/api/auth/${mode}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(values),
      });

      if (!res.ok) {
        const body = await res.json().catch(() => null);
        throw new ApiError(
          body?.error?.code ?? "INTERNAL",
          body?.error?.message ?? "Something went wrong.",
          res.status,
          body?.error?.details,
        );
      }

      toast.success(mode === "login" ? "Welcome back" : "Account created");

      // Honour ?next= from middleware, so a deep link survives the login bounce.
      router.push(safeNext(searchParams.get("next")));
      router.refresh();
    } catch (err) {
      const apiError = toApiError(err);

      // Put the message on the field the user has to fix. A toast for
      // "that email is taken" makes them hunt for which input is wrong.
      const fieldError = fieldErrorFrom(apiError, ["email", "password"]);
      if (fieldError) {
        form.setError(fieldError.field as keyof CredentialsInput, {
          type: "server",
          message: fieldError.message,
        });
        return;
      }

      // Bad credentials are deliberately not attributable to a single field —
      // the server refuses to say whether the email exists — so it goes above
      // the form, not next to the password input.
      setFormError(apiError.message);
    }
  }

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4" noValidate>
        {formError && (
          <div
            role="alert"
            className="border-destructive/50 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-sm"
          >
            {formError}
          </div>
        )}

        <FormField
          control={form.control}
          name="email"
          render={({ field }) => (
            <FormItem>
              <FormLabel>Email</FormLabel>
              <FormControl>
                <Input
                  type="email"
                  autoComplete="email"
                  placeholder="you@example.com"
                  autoFocus
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />

        <FormField
          control={form.control}
          name="password"
          render={({ field }) => (
            <FormItem>
              <FormLabel>Password</FormLabel>
              <FormControl>
                <Input
                  type="password"
                  // Tells password managers whether to offer to save or to fill.
                  autoComplete={mode === "login" ? "current-password" : "new-password"}
                  placeholder="••••••••"
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />

        <Button type="submit" className="w-full" disabled={isSubmitting}>
          {isSubmitting && <Loader2 className="size-4 animate-spin" aria-hidden />}
          {isSubmitting ? copy.pending : copy.submit}
        </Button>

        <p className="text-muted-foreground text-center text-sm">
          {copy.alt}{" "}
          <Link href={copy.altHref} className="text-foreground font-medium underline underline-offset-4">
            {copy.altLabel}
          </Link>
        </p>
      </form>
    </Form>
  );
}

// safeNext validates the ?next= redirect target. It must be a same-origin
// absolute PATH: exactly one leading slash, and the next character is neither a
// slash nor a backslash. That rejects "//evil.com" (protocol-relative) and
// "/\evil.com" — which browsers normalize to "//evil.com" — both open-redirect
// vectors a plain `startsWith("/") && !startsWith("//")` check misses.
export function safeNext(next: string | null): string {
  const fallback = "/dashboard/links";
  if (!next || !/^\/[^/\\]/.test(next)) return fallback;
  return next;
}
