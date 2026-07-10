import { redirect } from "next/navigation";

/**
 * The root is not a landing page. middleware.ts has already decided whether the
 * visitor has a session: with one, this redirect lands them on the dashboard;
 * without one, they never reach this component because middleware sent them to
 * /login first.
 */
export default function Home() {
  redirect("/dashboard/links");
}
