import { fileURLToPath } from "node:url";

// Static export: `NEXT_EXPORT=1 next build` outputs a pure static directory to web/out, which can be directly thrown into
// Run nginx web root directory. Development (next dev) does not set this variable, and retains /api reverse generation and hot update.
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel demo: The entire site is mocked, no backend, no need for /api reverse generation.
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

/** @type {import('next').NextConfig} */
const nextConfig = {
  // Avoid the lockfile of the parent directory from affecting root directory inference and resource path generation.
  turbopack: { root: fileURLToPath(new URL(".", import.meta.url)) },
  reactCompiler: true,
  // Allow access to dev resources (HMR) from LAN IP, add and delete as needed.
  // During the dev stage, any IPv4 source is allowed to access /_next/* and HMR (LAN IP changes are not affected).
  // Note: Next prohibits naked "*" for security reasons and requires segmented wildcarding; "*.*.*.*" matches any IPv4.
  allowedDevOrigins: ["*.*.*.*"],
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...(isExport
    ? {
        // Pure static export: no Node runtime; images are not optimized; each route outputs <route>/index.html.
        output: "export",
        images: { unoptimized: true },
        trailingSlash: true,
      }
    : isMock
      ? {
          // Vercel mock demo: no backend, no need for /api backend.
          images: { unoptimized: true },
        }
      : {
          // Development: Reverse /api/* to the Go backend (default: 8787, can be overridden with AUTOPENTEST_API).
          async rewrites() {
            const backend = process.env.AUTOPENTEST_API ?? "http://localhost:8787";
            return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
          },
        }),
};

export default nextConfig;
