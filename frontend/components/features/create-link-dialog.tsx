"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2, Plus } from "lucide-react";
import { useState } from "react";
import { useForm } from "react-hook-form";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { useCreateLink } from "@/hooks/use-links";
import { fieldErrorFrom, toApiError } from "@/lib/api-error";
import { createLinkSchema, type CreateLinkInput } from "@/lib/schemas";

/**
 * Client component: dialog open state, form state, mutation.
 *
 * shadcn's Dialog handles the a11y contract already — focus trap, focus restore
 * to the trigger on close, Escape to dismiss, aria-modal. Wrapping it would mean
 * reimplementing that, so it is used directly.
 */
export function CreateLinkDialog() {
  const [open, setOpen] = useState(false);
  const createLink = useCreateLink();

  const form = useForm<CreateLinkInput>({
    resolver: zodResolver(createLinkSchema),
    defaultValues: { url: "", alias: "", expiresAt: "" },
  });

  async function onSubmit(values: CreateLinkInput) {
    try {
      await createLink.mutateAsync({
        url: values.url,
        alias: values.alias?.trim() || undefined,
        // datetime-local is local wall-clock time; new Date() interprets it in the
        // browser's zone and toISOString() sends UTC, so the server stores the
        // instant the user meant regardless of where they are.
        expires_at: values.expiresAt ? new Date(values.expiresAt).toISOString() : undefined,
      });
      form.reset();
      setOpen(false);
    } catch (err) {
      const apiError = toApiError(err);

      // ALIAS_TAKEN belongs under the alias input, not in a toast the user has
      // to read and then map back to a field themselves.
      const fieldError = fieldErrorFrom(apiError, ["alias", "url"]);
      if (fieldError) {
        form.setError(fieldError.field as keyof CreateLinkInput, {
          type: "server",
          message: fieldError.message,
        });
        return;
      }
      // Anything else already surfaced as a toast from the mutation's onError.
    }
  }

  function onOpenChange(next: boolean) {
    setOpen(next);
    // Reset on close so reopening does not show the previous attempt's errors.
    if (!next) form.reset();
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button>
          <Plus className="size-4" aria-hidden />
          New link
        </Button>
      </DialogTrigger>

      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Create a short link</DialogTitle>
          <DialogDescription>
            Paste a destination URL. Leave the alias blank to get a random code.
          </DialogDescription>
        </DialogHeader>

        <Form {...form}>
          <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4" noValidate>
            <FormField
              control={form.control}
              name="url"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Destination URL</FormLabel>
                  <FormControl>
                    <Input
                      // type="url" would enable the browser's own validation
                      // bubble, which competes with the zod message below.
                      type="text"
                      inputMode="url"
                      placeholder="https://example.com/a/very/long/path"
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
              name="alias"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    Custom alias{" "}
                    <span className="text-muted-foreground font-normal">(optional)</span>
                  </FormLabel>
                  <FormControl>
                    <Input placeholder="my-link" autoComplete="off" {...field} />
                  </FormControl>
                  <FormDescription>
                    3–32 characters: letters, digits, hyphen or underscore.
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name="expiresAt"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    Expires{" "}
                    <span className="text-muted-foreground font-normal">(optional)</span>
                  </FormLabel>
                  <FormControl>
                    <Input
                      type="datetime-local"
                      // Cannot pick a past instant in the picker itself; the zod
                      // schema and the server enforce it for typed input.
                      min={localDateTimeNow()}
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>Leave blank for a link that never expires.</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={createLink.isPending}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={createLink.isPending}>
                {createLink.isPending && <Loader2 className="size-4 animate-spin" aria-hidden />}
                {createLink.isPending ? "Creating…" : "Create link"}
              </Button>
            </DialogFooter>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  );
}

// localDateTimeNow returns "YYYY-MM-DDTHH:mm" in the browser's local time, the
// format <input type="datetime-local"> expects for its `min`. toISOString would
// give UTC and mislabel the floor by the timezone offset.
function localDateTimeNow(): string {
  const now = new Date();
  const offsetMs = now.getTimezoneOffset() * 60_000;
  return new Date(now.getTime() - offsetMs).toISOString().slice(0, 16);
}
