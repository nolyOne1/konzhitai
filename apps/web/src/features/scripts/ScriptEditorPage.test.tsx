import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ScriptEditorPage } from './ScriptEditorPage'

const detail = {
  script: {
    id: 'script-1',
    name: '数据归档',
    description: '归档每日数据',
    runtime: 'bash',
    category: '数据处理',
    tags: ['生产', '每日'],
    currentVersionId: 'version-1',
    currentVersion: 1,
    draftUpdatedAt: '2026-08-28T08:00:00Z',
    createdAt: '2026-08-28T07:00:00Z',
    updatedAt: '2026-08-28T08:00:00Z',
  },
  draft: {
    scriptId: 'script-1',
    baseVersionId: 'version-1',
    content: 'echo "开始归档"\n',
    manifest: {
      runtime: 'bash',
      entrypoint: 'main.sh',
      category: '数据处理',
      tags: ['生产', '每日'],
      distribution: { mode: 'all_compatible', labels: {} },
      parameterDefinitions: [],
      resources: { cpuMillicores: 200, memoryBytes: 268435456, diskBytes: 536870912 },
    },
    updatedAt: '2026-08-28T08:00:00Z',
  },
  versions: [{
    id: 'version-1',
    scriptId: 'script-1',
    number: 1,
    artifactUri: 'scripts/script-1/hash.tar.gz',
    artifactSha256: 'a'.repeat(64),
    entrypoint: 'main.sh',
    manifest: {
      runtime: 'bash',
      entrypoint: 'main.sh',
      category: '数据处理',
      tags: ['生产'],
      distribution: { mode: 'all_compatible', labels: {} },
      parameterDefinitions: [],
      resources: { cpuMillicores: 200, memoryBytes: 268435456, diskBytes: 536870912 },
    },
    releaseNotes: '首次发布脚本',
    createdBy: '开发者',
    createdAt: '2026-08-28T08:00:00Z',
  }],
}

describe('脚本编辑器', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('编辑草稿、配置发布目标并要求中文发布说明', async () => {
    const fetchMock = vi.fn().mockImplementation(async (path: string, init?: RequestInit) => {
      if (path === '/api/scripts/script-1' && !init?.method) return response(detail)
      if (path === '/api/scripts/script-1/draft' && init?.method === 'PUT') return response(detail.draft)
      if (path === '/api/scripts/script-1/publish' && init?.method === 'POST') {
        return response({ ...detail.versions[0], id: 'version-2', number: 2, releaseNotes: '增加归档校验' })
      }
      throw new Error(`未处理的请求：${path}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    renderEditor()

    expect(await screen.findByRole('heading', { name: '编辑数据归档' })).toBeVisible()
    expect(screen.getByLabelText('脚本内容')).toHaveValue('echo "开始归档"\n')
    expect(screen.getByLabelText('发布目标')).toHaveValue('all_compatible')
    expect(screen.getByText('版本 1')).toBeVisible()

    await user.type(screen.getByLabelText('脚本内容'), 'echo "完成"\n')
    await user.click(screen.getByRole('button', { name: '保存草稿' }))
    expect(await screen.findByText('草稿已保存')).toBeVisible()

    await user.click(screen.getByRole('button', { name: '发布版本' }))
    expect(screen.getByRole('dialog', { name: '发布脚本版本' })).toBeVisible()
    await user.type(screen.getByLabelText('中文发布说明'), 'release two')
    await user.click(screen.getByRole('button', { name: '确认发布' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('发布说明必须包含中文')

    await user.clear(screen.getByLabelText('中文发布说明'))
    await user.type(screen.getByLabelText('中文发布说明'), '增加归档校验')
    await user.click(screen.getByRole('button', { name: '确认发布' }))
    expect(await screen.findByText('版本 2')).toBeVisible()

    const draftCall = fetchMock.mock.calls.find((call) => call[0] === '/api/scripts/script-1/draft')
    expect(draftCall?.[1]).toMatchObject({ method: 'PUT' })
    const publishCall = fetchMock.mock.calls.find((call) => call[0] === '/api/scripts/script-1/publish')
    expect(publishCall?.[1]).toMatchObject({ method: 'POST' })
  })

  it('回滚发布成功后用历史内容更新编辑器', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async (path: string, init?: RequestInit) => {
      if (path === '/api/scripts/script-1' && !init?.method) return response(detail)
      if (path === '/api/scripts/script-1/versions/version-1/content') return response({ content: 'echo "历史稳定版本"\n' })
      if (path === '/api/scripts/script-1/rollback' && init?.method === 'POST') {
        return response({ ...detail.versions[0], id: 'version-2', number: 2, releaseNotes: '回滚到稳定版本' })
      }
      throw new Error(`未处理的请求：${path}`)
    }))
    const user = userEvent.setup()
    renderEditor()

    await screen.findByRole('heading', { name: '编辑数据归档' })
    await user.click(screen.getByRole('button', { name: '回滚到此版本' }))
    await user.type(screen.getByLabelText('中文回滚说明'), '回滚到稳定版本')
    await user.click(screen.getByRole('button', { name: '确认回滚并发布' }))

    expect(await screen.findByText('已回滚并发布为版本 2')).toBeVisible()
    expect(screen.getByLabelText('脚本内容')).toHaveValue('echo "历史稳定版本"\n')
  })
})

function renderEditor() {
  return render(
    <MemoryRouter initialEntries={['/scripts/script-1']}>
      <Routes>
        <Route path="/scripts/:id" element={<ScriptEditorPage />} />
      </Routes>
    </MemoryRouter>,
  )
}

function response(value: unknown) {
  return Promise.resolve({ ok: true, json: async () => value })
}
