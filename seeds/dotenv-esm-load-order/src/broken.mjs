// BROKEN — the exact code that works in CommonJS and silently fails in ESM.
//
// ESM hoists every `import` and evaluates the whole module graph BEFORE a
// single statement of this file's body runs. So `./settings.mjs` reads
// process.env.CSX_DEMO_API_BASE while it is still unset, and captures
// undefined — even though .env exists, is found, and parses fine.
//
// In CommonJS the identical logic works, because `require()` executes at the
// point it appears:
//     require('dotenv').config();          // runs first
//     const { apiBase } = require('./settings.cjs');  // then this
//
// That asymmetry is why this costs people an afternoon: the .env is right
// there, dotenv reports no error, and the value is still undefined.
import dotenv from 'dotenv';
import { apiBase } from './settings.mjs';
import { report } from './report.mjs';

// Runs too late: ./settings.mjs was already evaluated during the hoisted
// import phase above.
dotenv.config();

report('broken', apiBase);
