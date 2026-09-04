import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactStrictMode: true,
  serverExternalPackages: ["@grpc/grpc-js", "@grpc/proto-loader", "ws"],
};

export default nextConfig;
