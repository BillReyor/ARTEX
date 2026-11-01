// Mock switch. Public variables injected during build time (the NEXT_PUBLIC_ prefix is ​​only readable by the browser).
// Set NEXT_PUBLIC_MOCK=1 on Vercel, which means the entire site will be mocked without a backend.
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
