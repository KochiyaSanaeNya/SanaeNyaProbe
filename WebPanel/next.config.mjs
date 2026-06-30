/** @type {import('next').NextConfig} */
const nextConfig = {
  outputFileTracingIncludes: {
    "/api/monitor": ["./target.conf"],
  },
};

export default nextConfig;
