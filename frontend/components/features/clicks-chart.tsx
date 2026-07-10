"use client";

import {
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import { useIsDarkTheme } from "@/hooks/use-is-dark-theme";
import type { DailyClicks } from "@/types/api";

/**
 * Clicks per day.
 *
 * Form: bars, not a line. The data is a count per discrete UTC day — a
 * magnitude per bucket. A line implies a continuous quantity sampled over time
 * and interpolates between points, which would draw a slope through days that
 * simply had a whole number of clicks.
 *
 * One series, so there is no legend: the card title names it. Color comes from
 * --chart-1, whose light and dark steps were each validated against their own
 * surface (see the comment in globals.css).
 */
export function ClicksChart({ data }: { data: DailyClicks[] }) {
  const isDark = useIsDarkTheme();

  // Recharts needs concrete colors, not `var(--chart-1)` — it passes the value
  // straight to an SVG `fill`, and a CSS variable resolves there but cannot be
  // read back for the tooltip and axis ticks. Resolving once here keeps the
  // chart and the tokens from drifting apart.
  const seriesColor = isDark ? "#3b82f6" : "#2563eb";
  const axisColor = isDark ? "oklch(0.708 0 0)" : "oklch(0.556 0 0)"; // --muted-foreground
  const gridColor = isDark ? "oklch(1 0 0 / 10%)" : "oklch(0.922 0 0)"; // --border

  const total = data.reduce((sum, d) => sum + d.clicks, 0);

  // A chart of nothing is worse than a sentence saying so.
  if (total === 0) {
    return (
      <div className="text-muted-foreground flex h-70 items-center justify-center rounded-lg border border-dashed text-sm">
        No clicks recorded in this period.
      </div>
    );
  }

  return (
    <>
      <div className="h-70 w-full">
        <ResponsiveContainer width="100%" height="100%">
          <BarChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: -16 }}>
            {/* Recessive: horizontal rules only. Vertical gridlines add ink and
                say nothing that the category axis does not already say. */}
            <CartesianGrid stroke={gridColor} strokeDasharray="3 3" vertical={false} />

            <XAxis
              dataKey="day"
              tickFormatter={formatDayTick}
              tick={{ fill: axisColor, fontSize: 12 }}
              axisLine={false}
              tickLine={false}
              // Let Recharts drop ticks rather than overlap them at 30d/all.
              minTickGap={24}
            />
            <YAxis
              // Click counts are integers; a "2.5 clicks" tick is nonsense.
              allowDecimals={false}
              width={44}
              tick={{ fill: axisColor, fontSize: 12 }}
              axisLine={false}
              tickLine={false}
            />

            <Tooltip
              cursor={{ fill: gridColor, opacity: 0.4 }}
              content={<ChartTooltip />}
            />

            <Bar
              dataKey="clicks"
              fill={seriesColor}
              // Rounded data-end anchored to the baseline: [tl, tr, br, bl].
              radius={[4, 4, 0, 0]}
              maxBarSize={40}
            />
          </BarChart>
        </ResponsiveContainer>
      </div>

      {/* Identity and values never depend on reading the chart. This is the
          accessible equivalent, and it is what a screen reader announces. */}
      <details className="mt-4">
        <summary className="text-muted-foreground hover:text-foreground cursor-pointer text-xs">
          View as table
        </summary>
        <div className="mt-2 max-h-48 overflow-y-auto rounded-md border">
          <table className="w-full text-sm">
            <caption className="sr-only">Clicks per day</caption>
            <thead className="bg-muted/50 sticky top-0">
              <tr>
                <th scope="col" className="px-3 py-1.5 text-left font-medium">Day (UTC)</th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">Clicks</th>
              </tr>
            </thead>
            <tbody>
              {data.map((d) => (
                <tr key={d.day} className="border-t">
                  <td className="px-3 py-1.5 font-mono text-xs">{d.day}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{d.clicks}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </>
  );
}

interface TooltipPayload {
  payload: DailyClicks;
}

function ChartTooltip({
  active,
  payload,
}: {
  active?: boolean;
  payload?: TooltipPayload[];
}) {
  if (!active || !payload?.length) return null;
  const { day, clicks } = payload[0].payload;

  return (
    <div className="bg-popover text-popover-foreground rounded-md border px-3 py-2 shadow-md">
      <p className="text-muted-foreground text-xs">{formatDayFull(day)}</p>
      {/* The number wears text ink, not the series color. */}
      <p className="text-sm font-medium tabular-nums">
        {clicks.toLocaleString()} {clicks === 1 ? "click" : "clicks"}
      </p>
    </div>
  );
}

/**
 * `day` is "YYYY-MM-DD", a UTC calendar day — not an instant. Appending T00:00:00Z
 * and formatting in UTC is what stops a user in UTC-5 from seeing every bar
 * labelled with the previous day.
 */
function formatDayTick(day: string): string {
  return new Date(`${day}T00:00:00Z`).toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  });
}

function formatDayFull(day: string): string {
  return new Date(`${day}T00:00:00Z`).toLocaleDateString("en-US", {
    weekday: "short",
    month: "short",
    day: "numeric",
    year: "numeric",
    timeZone: "UTC",
  });
}
