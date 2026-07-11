import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  /**
   * Emits .next/standalone: a self-contained server bundle with only the
   * node_modules it actually imports. The Docker runtime stage copies that
   * instead of the full dependency tree, which is the difference between a
   * ~1.5GB image and a ~200MB one.
   *
   * Disabled on Vercel. Vercel compiles the app into its own serverless output
   * and does not run our server.js; asking for standalone there produces a
   * bundle nothing uses and can confuse the build. VERCEL=1 is set by their
   * build environment.
   */
  output: process.env.VERCEL ? undefined : "standalone",

  // The reverse proxy / load balancer sets these; do not advertise the stack.
  poweredByHeader: false,

  // Fail the production build on a type error rather than shipping it.
  // Next 16 removed the `eslint` key from NextConfig; linting is `npm run lint`.
  typescript: { ignoreBuildErrors: false },
};

export default nextConfig;
