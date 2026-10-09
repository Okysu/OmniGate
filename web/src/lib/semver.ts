// Mirrors server/internal/plugin/manifest.go CompareSemver: numeric
// major.minor.patch; a pre-release sorts before its release.
function parts(v: string): [number, number, number] {
  const core = v.split('-')[0] ?? ''
  const [a = '0', b = '0', c = '0'] = core.split('.')
  return [Number.parseInt(a, 10) || 0, Number.parseInt(b, 10) || 0, Number.parseInt(c, 10) || 0]
}

export function compareSemver(a: string, b: string): -1 | 0 | 1 {
  const pa = parts(a)
  const pb = parts(b)
  for (let i = 0; i < 3; i++) {
    if (pa[i]! !== pb[i]!)
      return pa[i]! < pb[i]! ? -1 : 1
  }
  const ra = a.includes('-')
  const rb = b.includes('-')
  if (ra && !rb)
    return -1
  if (!ra && rb)
    return 1
  return a < b ? -1 : a > b ? 1 : 0
}
