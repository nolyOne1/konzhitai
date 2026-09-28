// Compare only stable versions supported by the ordinary upgrade API.
// null means the direction is unknown, not that the versions are equal.
export function compareAgentVersions(left?: string, right?: string): -1 | 0 | 1 | null {
  const leftParts = stableVersionParts(left)
  const rightParts = stableVersionParts(right)
  if (!leftParts || !rightParts) return null
  for (let index = 0; index < 3; index++) {
    const a = leftParts[index]
    const b = rightParts[index]
    if (a.length !== b.length) return a.length < b.length ? -1 : 1
    if (a !== b) return a < b ? -1 : 1
  }
  return 0
}

function stableVersionParts(version?: string): string[] | null {
  if (!version || version.length > 64) return null
  const match = /^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.exec(version)
  // JavaScript's $ also matches before a final newline; require the whole input.
  return match && match[0] === version ? match.slice(1) : null
}
