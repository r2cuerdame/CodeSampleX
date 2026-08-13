// FIX 2 — keep dotenv.config() explicit (when you need its return value or
// options such as { path }), then reach the env-reading module with a dynamic
// import().
//
// Unlike a static import, `await import(...)` is evaluated at the point it
// appears, so ./settings.mjs is evaluated after process.env is populated.
import dotenv from 'dotenv';
import { report } from './report.mjs';

const result = dotenv.config();
if (result.error) throw result.error;

// Evaluated here, not hoisted — this is what makes the fix work.
const { apiBase } = await import('./settings.mjs');

report('fixed-dynamic-import', apiBase);
