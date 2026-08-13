import { ESLint } from 'eslint';

// eslint 9+ reads eslint.config.js (flat) and IGNORES .eslintrc entirely —
// an old config is not merged, it is invisible, so the linter silently
// applies no rules at all. Flat config is a plain array, and passing it as
// overrideConfig with overrideConfigFile: true is how you use one without
// a file on disk.
const config = [{
  files: ['**/*.js'],
  languageOptions: { ecmaVersion: 2022, sourceType: 'module' },
  rules: { eqeqeq: 2, 'no-unused-vars': 1 },
}];

export async function lint(code) {
  const eslint = new ESLint({ overrideConfigFile: true, overrideConfig: config });
  const [result] = await eslint.lintText(code, { filePath: 'sample.js' });
  return result;
}
