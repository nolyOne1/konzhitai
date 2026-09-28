import { describe, expect, it } from 'vitest'

import { compareAgentVersions } from './agentVersion'

describe('普通代理升级的稳定版本比较', () => {
  it.each([
    ['0.2.6', '0.2.0', 1],
    ['0.2.9', '0.2.10', -1],
    ['v0.2.0', '0.2.0', 0],
    ['0.0.0', 'v0.0.0', 0],
    ['1.0.0', '0.999.999', 1],
    ['1.9007199254740993.0', '1.9007199254740992.0', 1],
    [`${'1'.repeat(60)}.0.0`, '1.0.0', 1],
    [`v${'1'.repeat(59)}.0.0`, '1.0.0', 1],
  ] as const)('%s 与 %s 的方向是 %s', (left, right, expected) => {
    expect(compareAgentVersions(left, right)).toBe(expected)
    expect(compareAgentVersions(right, left)).toBe(expected === 0 ? 0 : -expected)
  })

  it.each([
    undefined, '', 'unknown', 'custom', '0.2.0-rc.1', '0.2.0+build.1',
    '01.2.3', '1.02.3', '1.2.03', 'V1.2.3', 'vv1.2.3', '1.2', '1.2.3.4',
    '-1.2.3', '+1.2.3', '１.2.3', ' 1.2.3', '1.2.3 ', '1.2.3\n', '1.2.3\r\n',
    `v${'1'.repeat(60)}.0.0`,
  ])('版本 %s 不猜测方向', (version) => {
    expect(compareAgentVersions(version, '1.2.3')).toBeNull()
    expect(compareAgentVersions('1.2.3', version)).toBeNull()
    expect(compareAgentVersions(version, version)).toBeNull()
  })
})
