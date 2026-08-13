// FIX 1 (preferred) — load dotenv through its side-effect entry point, as the
// FIRST import in the file.
//
// `dotenv/config` calls config() during its OWN evaluation, and static imports
// are evaluated in source order. So process.env is fully populated before
// ./settings.mjs is evaluated, and the import-time read sees the real value.
//
// Keep this import first, and keep it side-effect style — `import dotenv from
// 'dotenv'` followed by dotenv.config() in the body is exactly the broken
// version, because the body runs last. Linters that sort imports
// alphabetically can silently reintroduce the bug.
import 'dotenv/config';
import { apiBase } from './settings.mjs';
import { report } from './report.mjs';

report('fixed-side-effect-import', apiBase);
