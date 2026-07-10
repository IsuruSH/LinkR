import { z } from "zod";

/**
 * Client-side validation schemas.
 *
 * These mirror the server's rules but are not a substitute for them: the server
 * validates everything again, because a client check is a courtesy to the user,
 * not a security control. Where the two must agree exactly (bcrypt's 72-byte
 * ceiling, the alias character class) the constraint is written down in both
 * places and noted here.
 */

export const credentialsSchema = z.object({
  email: z.string().min(1, "Email is required").email("Enter a valid email address"),
  password: z
    .string()
    .min(8, "Password must be at least 8 characters")
    // bcrypt silently truncates at 72 bytes; the server rejects rather than
    // truncate, so tell the user here instead of letting the request fail.
    .max(72, "Password must be at most 72 characters"),
});

export type CredentialsInput = z.infer<typeof credentialsSchema>;

/** Matches domain.aliasPattern and the links_short_code_format CHECK constraint. */
const ALIAS_PATTERN = /^[A-Za-z0-9_-]{3,32}$/;

export const createLinkSchema = z.object({
  url: z
    .string()
    .min(1, "URL is required")
    .max(2048, "URL must be at most 2048 characters")
    .refine(
      (value) => {
        try {
          const parsed = new URL(value);
          return parsed.protocol === "http:" || parsed.protocol === "https:";
        } catch {
          return false;
        }
      },
      // Naming the accepted schemes is more useful than "invalid URL", because
      // the most common mistake is pasting "example.com" with no scheme at all.
      { message: "Must be an absolute http:// or https:// URL" },
    ),
  alias: z
    .string()
    .trim()
    .refine((value) => value === "" || ALIAS_PATTERN.test(value), {
      message: "3–32 characters: letters, digits, hyphen or underscore",
    })
    .optional(),
});

export type CreateLinkInput = z.infer<typeof createLinkSchema>;
