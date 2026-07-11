/**
 * Wire types. These mirror the Go DTOs in backend/internal/handler/dto.go.
 *
 * They are hand-written rather than generated: the API surface is six endpoints,
 * and a codegen pipeline for that is more machinery than it saves. If this grew,
 * the honest answer is to emit an OpenAPI spec from the Go handlers.
 */

export interface Link {
  short_code: string;
  short_url: string;
  long_url: string;
  click_count: number;
  created_at: string;
  /** null when the link never expires. */
  expires_at: string | null;
}

export interface LinkPage {
  items: Link[];
  /** null on the last page. Opaque — never parse or construct one. */
  next_cursor: string | null;
}

export interface DailyClicks {
  /** A UTC calendar day, "YYYY-MM-DD". Not an instant: do not `new Date()` it
   *  without care, or a user west of UTC sees every bar shifted a day back. */
  day: string;
  clicks: number;
}

export type StatsRange = "7d" | "30d" | "all";

export interface LinkStats {
  short_code: string;
  short_url: string;
  long_url: string;
  /** Lifetime, from the denormalized counter — deliberately NOT the sum of
   *  `series`, which is windowed by `range`. */
  total_clicks: number;
  range: StatsRange;
  series: DailyClicks[];
}

export interface AuthUser {
  id: string;
  email: string;
}

export interface TokenResponse {
  access_token: string;
  token_type: "Bearer";
  expires_at: string;
  user: AuthUser;
}

/** The error envelope every failing endpoint returns. */
export interface ApiErrorBody {
  error: {
    code: ApiErrorCode;
    message: string;
    /** Field-level detail, e.g. `{ url: "must be http or https" }`. */
    details?: Record<string, string>;
  };
}

/**
 * Stable machine codes from backend/internal/domain/errors.go. Switching on
 * these rather than on the message means copy edits never break the UI.
 */
export type ApiErrorCode =
  | "INTERNAL"
  | "NOT_FOUND"
  | "METHOD_NOT_ALLOWED"
  | "VALIDATION_FAILED"
  | "LINK_NOT_FOUND"
  | "LINK_EXPIRED"
  | "ALIAS_TAKEN"
  | "INVALID_URL"
  | "INVALID_ALIAS"
  | "RESERVED_ALIAS"
  | "EMAIL_TAKEN"
  | "INVALID_CREDENTIALS"
  | "UNAUTHORIZED"
  | "INVALID_CURSOR"
  | "CODE_GENERATION_FAILED"
  | "RATE_LIMITED";
