import axios from "axios";

import type { ApiErrorBody, ApiErrorCode } from "@/types/api";

/**
 * ApiError is the single error type the UI reasons about. Every axios failure —
 * a 409 from the backend, a network drop, a timeout — becomes one of these, so
 * no component ever inspects `err.response?.data?.error?.code` itself.
 */
export class ApiError extends Error {
  readonly code: ApiErrorCode;
  readonly status: number;
  readonly details?: Record<string, string>;

  constructor(
    code: ApiErrorCode,
    message: string,
    status: number,
    details?: Record<string, string>,
  ) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.details = details;
  }

  /** True when retrying the same request could plausibly succeed. Drives the
   *  retry policy in the query client and the "Try again" buttons. */
  get isRetryable(): boolean {
    return this.status === 0 || this.status >= 500;
  }

  get isAuthError(): boolean {
    return this.status === 401;
  }
}

function isApiErrorBody(data: unknown): data is ApiErrorBody {
  return (
    typeof data === "object" &&
    data !== null &&
    "error" in data &&
    typeof (data as ApiErrorBody).error?.code === "string"
  );
}

/**
 * Normalizes anything thrown by axios into an ApiError.
 *
 * A network failure has no response at all. Mapping it to status 0 rather than
 * letting `undefined` propagate is what keeps `isRetryable` from silently
 * returning false on exactly the case most worth retrying.
 */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;

  if (axios.isAxiosError(err)) {
    const status = err.response?.status ?? 0;
    const data = err.response?.data;

    if (isApiErrorBody(data)) {
      return new ApiError(data.error.code, data.error.message, status, data.error.details);
    }
    if (status === 0) {
      return new ApiError("INTERNAL", "Could not reach the server. Check your connection.", 0);
    }
    return new ApiError("INTERNAL", err.message || "Something went wrong.", status);
  }

  return new ApiError(
    "INTERNAL",
    err instanceof Error ? err.message : "Something went wrong.",
    0,
  );
}

/**
 * Maps a field-level validation error onto a form field, so the message lands
 * next to the input the user has to fix rather than in a toast they must
 * remember. Returns null when the error is not attributable to one field.
 */
export function fieldErrorFrom(
  err: ApiError,
  fields: readonly string[],
): { field: string; message: string } | null {
  if (err.details) {
    for (const field of fields) {
      const message = err.details[field];
      if (message) return { field, message };
    }
  }

  // Some errors are semantically about a field but carry no details map.
  switch (err.code) {
    case "ALIAS_TAKEN":
    case "RESERVED_ALIAS":
    case "INVALID_ALIAS":
      return fields.includes("alias") ? { field: "alias", message: err.message } : null;
    case "INVALID_URL":
      return fields.includes("url") ? { field: "url", message: err.message } : null;
    case "EMAIL_TAKEN":
      return fields.includes("email") ? { field: "email", message: err.message } : null;
    default:
      return null;
  }
}
