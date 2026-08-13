import YAML from 'yaml';

// yaml v2 follows YAML 1.2: only true/false are booleans, so "yes" and "on"
// stay strings — the opposite of the YAML 1.1 behaviour older tools had.
// Values come back as plain JS, so a round trip is safe to compare directly.
export function load(text) {
  return YAML.parse(text);
}

export function roundTrip(value) {
  return YAML.parse(YAML.stringify(value));
}
