export const PROJECT_NAME_MIN = 2;
export const PROJECT_NAME_MAX = 100;

// The API counts characters (runes), not UTF-16 units, so count code points.
export function projectNameLength(raw: string): number {
  return [...raw.trim()].length;
}

// The create button is enabled only for a trimmed name of 2-100 characters.
export function isValidProjectName(raw: string): boolean {
  const n = projectNameLength(raw);
  return n >= PROJECT_NAME_MIN && n <= PROJECT_NAME_MAX;
}
