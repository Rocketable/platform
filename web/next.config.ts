import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactStrictMode: true,
  serverExternalPackages: ["@grpc/grpc-js", "@grpc/proto-loader"],
  allowedDevOrigins: ["100.95.197.99", "100.96.54.117"],
};

export default nextConfig;
