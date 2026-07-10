"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";

/**
 * Copy-to-clipboard with a transient "copied" state.
 *
 * navigator.clipboard is undefined on insecure origins other than localhost, so
 * a deployment on plain HTTP would throw a TypeError on click. Fall back to the
 * deprecated execCommand path rather than doing nothing.
 */
export function useCopyToClipboard(resetAfterMs = 1500) {
  const [copiedValue, setCopiedValue] = useState<string | null>(null);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Clear the timer on unmount: a table row can be removed (deleted link) while
  // its "copied" timeout is still pending, and setting state then warns.
  useEffect(() => {
    return () => {
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
    };
  }, []);

  const copy = useCallback(
    async (value: string) => {
      const ok = await writeToClipboard(value);
      if (!ok) {
        toast.error("Could not copy to clipboard");
        return;
      }

      setCopiedValue(value);
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
      timeoutRef.current = setTimeout(() => setCopiedValue(null), resetAfterMs);
    },
    [resetAfterMs],
  );

  return { copy, copiedValue };
}

async function writeToClipboard(value: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return true;
    } catch {
      // Permission denied, or the document is not focused. Fall through.
    }
  }

  try {
    const textarea = document.createElement("textarea");
    textarea.value = value;
    // Keep it off-screen and unfocusable-looking, but still selectable.
    textarea.setAttribute("readonly", "");
    textarea.style.position = "fixed";
    textarea.style.opacity = "0";
    document.body.appendChild(textarea);
    textarea.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(textarea);
    return ok;
  } catch {
    return false;
  }
}
