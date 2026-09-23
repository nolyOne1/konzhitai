import { withSession } from '../../test/session'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { TaskSchedulesEditor } from './TaskSchedulesEditor'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

it('读取既有计划并保存编辑、停用和删除结果', async () => {
  let schedule = { id: 'schedule-1', definitionId: 'task-1', cronExpression: '0 2 * * *', timezone: 'Asia/Shanghai', enabled: true, nextRunAt: '2026-08-29T02:00:00+08:00' }
  const fetchMock = vi.fn(async (path: string, init?: RequestInit) => {
    if (path === '/api/tasks/task-1/schedules' && !init?.method) return response({ schedules: [schedule] })
    if (path === '/api/tasks/cron/validate') return response({ valid: true })
    if (path === '/api/tasks/task-1/schedules/schedule-1' && init?.method === 'PUT') {
      schedule = { ...schedule, ...JSON.parse(init.body as string) }
      return response(schedule)
    }
    if (path === '/api/tasks/task-1/schedules/schedule-1' && init?.method === 'DELETE') return response(null, 204)
    throw new Error(`意外请求 ${path}`)
  })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()
  render(withSession(<TaskSchedulesEditor taskId="task-1" />))
  expect(await screen.findByText('0 2 * * *')).toBeVisible()
  await user.click(screen.getByRole('button', { name: '编辑计划 0 2 * * *' }))
  expect(screen.getByLabelText('计划 Cron 表达式')).toHaveValue('0 2 * * *')
  await user.clear(screen.getByLabelText('计划 Cron 表达式'))
  await user.type(screen.getByLabelText('计划 Cron 表达式'), '0 3 * * *')
  await user.click(screen.getByRole('button', { name: '保存计划' }))
  expect(await screen.findByText('0 3 * * *')).toBeVisible()
  expect(fetchMock.mock.calls.some(([path, init]) => path.endsWith('/schedules/schedule-1') && init?.method === 'PUT' && JSON.parse(init.body as string).cronExpression === '0 3 * * *')).toBe(true)
  await user.click(screen.getByRole('button', { name: '停用计划 0 3 * * *' }))
  expect(await screen.findByText('已停用')).toBeVisible()
  await user.click(screen.getByRole('button', { name: '删除计划 0 3 * * *' }))
  await user.click(screen.getByRole('button', { name: '确认删除计划' }))
  expect(await screen.findByText('尚未配置计划，可继续手动执行任务。')).toBeVisible()
})

it('旧记录含浏览器不支持的时区时保留计划并显示UTC回退时间', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => response({ schedules: [{ id: 'legacy', definitionId: 'task-1', cronExpression: '0 2 * * *', timezone: 'Local', enabled: true, nextRunAt: '2026-08-29T02:00:00+08:00' }] })))
  render(withSession(<TaskSchedulesEditor taskId="task-1" />))
  expect(await screen.findByText(/18:00:00 UTC（原时区：Local）/)).toBeVisible()
  expect(screen.getByRole('button', { name: '编辑计划 0 2 * * *' })).toBeEnabled()
})

function response(value: unknown, status = 200) { return { ok: status >= 200 && status < 300, status, json: async () => value } }
