// An ordinary config module. It reads process.env once, at import time, and
// exports the value — which is what almost every config/client module does.
//
// Nothing in this file is wrong. The bug is *when* this file gets evaluated
// relative to dotenv.config(). See src/broken.mjs.
export const apiBase = process.env.CSX_DEMO_API_BASE;
