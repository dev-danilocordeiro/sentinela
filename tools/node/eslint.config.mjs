// Config neutra do scanner eslint do Sentinela (ADR 0060). Roda no lugar da config do repo
// analisado, sem instalar as dependências dele: só regras que apontam bug provável e não dependem
// de tipos (a descoberta não tem node_modules) nem do estilo do time.
// Regras: https://eslint.org/docs/latest/rules/, https://typescript-eslint.io/rules/ e
// https://react.dev/reference/eslint-plugin-react-hooks.
import js from "@eslint/js";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

export default tseslint.config(
  {
    ignores: [
      "**/node_modules/**", "**/dist/**", "**/build/**", "**/out/**", "**/.next/**",
      "**/coverage/**", "**/*.min.js", "**/*.d.ts", "**/*.generated.ts", "**/*.generated.tsx",
    ],
  },
  {
    files: ["**/*.{js,jsx,mjs,cjs,ts,tsx,mts,cts}"],
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    languageOptions: { parserOptions: { ecmaFeatures: { jsx: true } } },
    plugins: { "react-hooks": reactHooks },
    rules: {
      // Sem a lista de globais do ambiente (browser, node, jest), no-undef só daria ruído; o tsc
      // cobre nome indefinido em TypeScript.
      "no-undef": "off",
      // async sem await: costuma ser o await esquecido numa chamada que devolve Promise.
      "require-await": "error",
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "warn",
      // Estilo e migração, não bug.
      "@typescript-eslint/no-require-imports": "off",
      "@typescript-eslint/no-empty-object-type": "off",
      "@typescript-eslint/triple-slash-reference": "off",
    },
  },
  {
    // Nos testes, mock async sem await (async () => valor) e any em dublê são o normal, não bug:
    // no web do ceres, 5 dos 6 sinais eram require-await em mocks.
    files: [
      "**/*.{test,spec}.{js,jsx,mjs,cjs,ts,tsx,mts,cts}",
      "**/{__tests__,test,tests,e2e}/**",
    ],
    rules: {
      "require-await": "off",
      "@typescript-eslint/no-explicit-any": "off",
    },
  },
);
