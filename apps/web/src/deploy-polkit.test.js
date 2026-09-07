// @vitest-environment node

import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'

const { describe, it } = process.env.VITEST
  ? await import('vitest')
  : await import('node:test')

const result = {
  YES: 'yes',
  NO: 'no',
  NOT_HANDLED: 'not-handled',
}

function loadRule() {
  const source = readFileSync(
    new URL('../../../deploy/agent/50-yunling-agent.rules', import.meta.url),
    'utf8',
  )
  let loadedRule
  runInNewContext(source, {
    polkit: {
      Result: result,
      addRule(rule) {
        loadedRule = rule
      },
    },
  })
  return loadedRule
}

function action(unit, verb = 'start') {
  return {
    id: 'org.freedesktop.systemd1.manage-units',
    lookup(key) {
      return { unit, verb }[key]
    },
  }
}

describe('京东云代理 Polkit 规则', () => {
  it('允许专用代理用户启动固定任务模板实例', () => {
    const rule = loadRule()

    assert.equal(
      rule(action('yunling-run@31109d87-f742-41b0-b198-9c45de7dc287.service'), {
        user: 'yunling-agent',
      }),
      result.YES,
    )
  })

  it('只允许专用代理用户启动合法编号的升级单元', () => {
    const rule = loadRule()

    assert.equal(
      rule(action('yunling-agent-upgrade@upgrade_2026-09-07.service'), {
        user: 'yunling-agent',
      }),
      result.YES,
    )
    assert.equal(
      rule(action('yunling-agent-upgrade@upgrade-1.service', 'stop'), {
        user: 'yunling-agent',
      }),
      result.NO,
    )
    assert.equal(
      rule(action('yunling-agent-upgrade@../ssh.service'), {
        user: 'yunling-agent',
      }),
      result.NO,
    )
    assert.equal(
      rule(action(`yunling-agent-upgrade@${'a'.repeat(129)}.service`), {
        user: 'yunling-agent',
      }),
      result.NO,
    )
  })

  it('拒绝其他用户、其他单元和未授权动作', () => {
    const rule = loadRule()

    assert.equal(
      rule(action('yunling-run@run-1.service'), { user: 'other-user' }),
      result.NOT_HANDLED,
    )
    assert.equal(
      rule(action('ssh.service'), { user: 'yunling-agent' }),
      result.NO,
    )
    assert.equal(
      rule(action('yunling-run@run-1.service', 'restart'), {
        user: 'yunling-agent',
      }),
      result.NO,
    )
    assert.equal(
      rule(action('yunling-run@../ssh.service'), { user: 'yunling-agent' }),
      result.NO,
    )
  })
})
