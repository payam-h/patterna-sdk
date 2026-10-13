import js from "@eslint/js";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist/**", "src/generated/**", "node_modules/**"] },
  js.configs.recommended,
  ...tseslint.configs.strict,
  {
    rules: {
      "no-console": "error",
    },
  },
  {
    // Tests index into recorded calls they just asserted exist.
    files: ["test/**/*.ts"],
    rules: { "@typescript-eslint/no-non-null-assertion": "off" },
  },
  {
    files: ["src/**/*.ts"],
    rules: {
      // Node built-ins would break React Native and browsers.
      "no-restricted-imports": ["error", { patterns: ["node:*", "fs", "crypto", "http", "https", "buffer", "stream"] }],
    },
  },
);
