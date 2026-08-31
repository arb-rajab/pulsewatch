import js from '@eslint/js';
import svelte from 'eslint-plugin-svelte';
import tseslint from 'typescript-eslint';
import prettier from 'eslint-config-prettier';

export default tseslint.config(
	js.configs.recommended,
	...tseslint.configs.recommended,
	...svelte.configs['flat/recommended'],
	prettier,
	{
		languageOptions: {
			globals: {
				console: 'readonly'
			}
		}
	},
	{
		// svelte.configs['flat/recommended'] parses .svelte files with
		// svelte-eslint-parser, but that parser needs to be told to hand
		// <script lang="ts"> blocks to typescript-eslint's own parser itself
		// — without this, a Svelte 5 component using `import type` or
		// `$props()` type annotations fails with a bare "Unexpected token"
		// parse error, not a real lint finding. No .svelte file in this repo
		// used lang="ts" before this session, so the gap was never
		// surfaced.
		files: ['**/*.svelte'],
		languageOptions: {
			parserOptions: {
				parser: tseslint.parser
			}
		}
	},
	{
		ignores: ['build/', '.svelte-kit/', 'dist/']
	}
);
