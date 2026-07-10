import { LinkStats } from "@/components/features/link-stats";

/**
 * Server component. `params` is a Promise in the App Router, so it is awaited
 * here and the resolved code is handed to the client island that fetches.
 */
export async function generateMetadata({ params }: { params: Promise<{ code: string }> }) {
  const { code } = await params;
  return { title: `/${code} · Linkr` };
}

export default async function StatsPage({ params }: { params: Promise<{ code: string }> }) {
  const { code } = await params;
  return <LinkStats code={code} />;
}
