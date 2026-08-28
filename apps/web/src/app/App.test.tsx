import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { App } from './App'

describe('云令应用壳', () => {
  it('显示全中文控制台品牌和主导航', () => {
    render(<App />)

    expect(screen.getByText('云令')).toBeInTheDocument()
    expect(screen.getByText('脚本调度中心')).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: '主导航' })).toBeVisible()
    expect(screen.getByRole('link', { name: '运行总览' })).toBeVisible()
    expect(screen.getByRole('link', { name: '脚本中心' })).toBeVisible()
    expect(screen.getByRole('link', { name: '任务调度' })).toBeVisible()
    expect(screen.getByRole('link', { name: '执行记录' })).toBeVisible()
    expect(screen.getByRole('link', { name: '服务器' })).toBeVisible()
  })
})
