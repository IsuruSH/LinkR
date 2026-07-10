import { Suspense } from "react";

import { AuthForm } from "@/components/features/auth-form";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

export const metadata = { title: "Create account · Linkr" };

export default function RegisterPage() {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Create an account</CardTitle>
        <CardDescription>Start shortening links in a few seconds.</CardDescription>
      </CardHeader>
      <CardContent>
        <Suspense fallback={<FormSkeleton />}>
          <AuthForm mode="register" />
        </Suspense>
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
