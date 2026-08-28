import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { TaskEditorPage } from './TaskEditorPage'

const script = {
  id: 'script-1',
  name: '每日数据归档',
  description: '归档每日业务数据',
  runtime: 'bash',
  category: '数据处理',
  tags: ['生产'],
  currentVersionId: 'version-3',
  currentVersion: 3,
  draftUpdatedAt: '2026-08-28T08:00:00Z',
  createdAt: '2026-08-28T07:00:00Z',
  updatedAt: '2026-08-28T08:00:00Z',
}

describe('任务编辑器', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('配置完整调度规则并创建带上海时区的 Cron 计划', async () => {
    const fetchMock = vi.fn().mockImplementation(async (path: string, init?: RequestInit) => {
      if (path === '/api/scripts' && !init?.method) return response({ scripts: [script] })
      if (path === '/api/tasks' && init?.method === 'POST') return response({ id: 'task-1', name: '每日归档任务' }, 201)
      if (path === '/api/tasks/cron/validate' && init?.method === 'POST') return response({ valid: true })
      if (path === '/api/tasks/task-1/schedules' && init?.method === 'POST') return response({ id: 'schedule-1' }, 201)
      throw new Error(`未处理的请求：${path}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    renderEditor()

    expect(await screen.findByRole('heading', { name: '新建任务' })).toBeVisible()
    expect(screen.getByLabelText('CPU（毫核）')).toBeVisible()
    expect(screen.getByLabelText('内存（MB）')).toBeVisible()
    expect(screen.getByLabelText('磁盘（MB）')).toBeVisible()
    expect(screen.getByLabelText('最大等待（秒）')).toBeVisible()
    expect(screen.getByLabelText('最大并发')).toBeVisible()
    expect(screen.getByLabelText('幂等任务')).not.toBeChecked()

    await user.type(screen.getByLabelText('任务名称'), '每日归档任务')
    await user.selectOptions(screen.getByLabelText('执行脚本'), 'script-1')
    await user.clear(screen.getByLabelText('任务参数（JSON）'))
    await user.click(screen.getByLabelText('任务参数（JSON）'))
    await user.paste('{"保留天数":30}')
    await user.type(screen.getByLabelText('服务器标签'), '用途=批处理')
    await user.click(screen.getByLabelText('幂等任务'))
    await user.click(screen.getByLabelText('启用定时执行'))
    await user.click(screen.getByRole('button', { name: '创建任务' }))

    expect(await screen.findByText('任务已创建')).toBeVisible()
    const createCall = fetchMock.mock.calls.find((call) => call[0] === '/api/tasks' && call[1]?.method === 'POST')
    const createBody = JSON.parse(createCall?.[1]?.body as string)
    expect(createBody).toMatchObject({
      name: '每日归档任务',
      scriptId: 'script-1',
      versionPolicy: 'latest',
      parameters: { 保留天数: 30 },
      requiredLabels: { 用途: '批处理' },
      idempotent: true,
      maxConcurrency: 1,
      maxWaitSeconds: 86400,
    })
    const scheduleCall = fetchMock.mock.calls.find((call) => call[0] === '/api/tasks/task-1/schedules')
    expect(JSON.parse(scheduleCall?.[1]?.body as string)).toMatchObject({
      cronExpression: '0 2 * * *',
      timezone: 'Asia/Shanghai',
      enabled: true,
    })
  })

  it('在参数 JSON 无效时显示字段错误且不提交', async () => {
    const fetchMock = vi.fn().mockImplementation(async (path: string) => {
      if (path === '/api/scripts') return response({ scripts: [script] })
      throw new Error(`不应提交请求：${path}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    renderEditor()

    await screen.findByRole('heading', { name: '新建任务' })
    await user.type(screen.getByLabelText('任务名称'), '错误参数任务')
    await user.selectOptions(screen.getByLabelText('执行脚本'), 'script-1')
    await user.clear(screen.getByLabelText('任务参数（JSON）'))
    await user.click(screen.getByLabelText('任务参数（JSON）'))
    await user.paste('{bad json}')
    await user.click(screen.getByRole('button', { name: '创建任务' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('任务参数必须是 JSON 对象')
    expect(fetchMock.mock.calls.filter((call) => call[0] === '/api/tasks')).toHaveLength(0)
  })
})

function renderEditor() {
  return render(
    <MemoryRouter initialEntries={['/tasks/new']}>
      <Routes>
        <Route path="/tasks/new" element={<TaskEditorPage />} />
        <Route path="/tasks" element={<div>任务已创建</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

function response(value: unknown, status = 200) {
  return Promise.resolve({ ok: status >= 200 && status < 300, status, json: async () => value })
}
