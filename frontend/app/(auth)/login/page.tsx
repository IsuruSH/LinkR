import { Suspense } from "react";

import { AuthForm } from "@/components/features/auth-form";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

export const metadata = { title: "Sign in · Linkr" };

export default function LoginPage() {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Sign in</CardTitle>
        <CardDescription>Enter your credentials to reach your dashboard.</CardDescription>
      </CardHeader>
      <CardContent>
        {/* AuthForm reads ?next= via useSearchParams, which opts the subtree into
            client-side rendering. Without a Suspense boundary Next fails the
            production build rather than silently deopting the whole page. */}
        <Suspense fallback={<FormSkeleton />}>
          <AuthForm mode="login" />
        </Suspense>

        <p className="text-muted-foreground mt-6 rounded-md border border-dashed px-3 py-2 text-center text-xs">
          Demo account: <span className="font-mono">demo@linkr.dev</span> /{" "}
          <span className="font-mono">demo-password-123</span>
        </p>
      </CardContent>
    </Card>
  );
}

function FormSkeleton() {
  return (
    <div className="space-y-4">
      <Skeleton className="h-14 w-full" />
      <Skeleton className="h-14 w-full" />
      <Skeleton className="h-9 w-full" />
    </div>
  );
}
