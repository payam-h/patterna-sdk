import { defineConfig } from "tsup";

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm", "cjs"],
  dts: true,
  sourcemap: true,
  clean: true,
  target: "es2022",
  // The core must run on Hermes (React Native) and in browsers: no Node built-ins.
  platform: "neutral",
});
