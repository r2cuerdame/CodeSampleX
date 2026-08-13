import fs from 'fs-extra';

// outputJson is writeJson plus mkdir -p: the plain node equivalent throws
// ENOENT when the directory is missing, which is the single most common
// reason a "write the config file" step fails on a fresh machine.
export async function writeConfig(path, value) {
  await fs.outputJson(path, value, { spaces: 2 });
}

export async function readConfig(path) {
  return fs.readJson(path);
}
