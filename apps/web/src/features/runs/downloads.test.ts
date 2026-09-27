import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { decodeTextPreview, DOWNLOAD_URL_LIFETIME_MS, previewUnavailableReason, saveBlob, TEXT_PREVIEW_LIMIT, validateDownloadType, verifyFile } from './downloads'

// jsdom's Blob lacks arrayBuffer and its Crypto lacks subtle. Use the runtime's
// real implementations without adding Node types to the browser application.
const { Blob: NodeBlob } = await vi.importActual<{ Blob: typeof Blob }>('node:buffer')
const { createHash, webcrypto } = await vi.importActual<{
  createHash: (algorithm: string) => { update: (text: string) => { digest: (format: 'hex') => string } }
  webcrypto: Crypto
}>('node:crypto')

beforeEach(() => {
  vi.stubGlobal('Blob', NodeBlob)
  vi.stubGlobal('crypto', webcrypto)
})
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

describe('已验证的文件下载和文本预览', () => {
  const text = '\uFEFF订单号,状态\n123,完成\n'
  const metadata = { byteSize: new TextEncoder().encode(text).byteLength, sha256: createHash('sha256').update(text).digest('hex') }

  it('校验原始 UTF-8 字节后解码，允许 BOM 且不改变下载字节', async () => {
    const blob = new Blob([text], { type: 'application/octet-stream' })
    const bytes = await verifyFile(blob, metadata, 'artifact')
    expect(Array.from(new Uint8Array(bytes))).toEqual(Array.from(new TextEncoder().encode(text)))
    expect(decodeTextPreview(bytes)).toBe(text.slice(1))
  })

  it.each([
    ['大小', { ...metadata, byteSize: metadata.byteSize - 1 }],
    ['SHA-256', { ...metadata, sha256: 'a'.repeat(64) }],
    ['校验信息', { ...metadata, sha256: '' }],
  ])('拒绝%s不一致的文件', async (message, expected) => {
    await expect(verifyFile(new Blob([text], { type: 'application/octet-stream' }), expected, 'artifact')).rejects.toThrow(message)
  })

  it.each(['text/html', 'application/json', ''])('即使摘要正确也拒绝错误 MIME %s', async (type) => {
    await expect(verifyFile(new Blob([text], { type }), metadata, 'artifact')).rejects.toThrow('响应类型不正确')
  })

  it('普通日志仅接受 text/plain，归档校验 gzip 大小和摘要', async () => {
    expect(() => validateDownloadType(new Blob(['log'], { type: 'text/plain; charset=utf-8' }), 'logs')).not.toThrow()
    expect(() => validateDownloadType(new Blob(['<html>'], { type: 'text/html' }), 'logs')).toThrow('响应类型')
    expect((await verifyFile(new Blob([text], { type: 'application/gzip' }), metadata, 'archive')).byteLength).toBe(metadata.byteSize)
    await expect(verifyFile(new Blob([text], { type: 'application/gzip' }), { ...metadata, sha256: '0'.repeat(64) }, 'archive')).rejects.toThrow('SHA-256')
  })

  it('预览只允许明确文本扩展名且最大 256 KiB', () => {
    expect(previewUnavailableReason({ name: '结果.CSV', byteSize: TEXT_PREVIEW_LIMIT })).toBe('')
    expect(previewUnavailableReason({ name: 'large.txt', byteSize: TEXT_PREVIEW_LIMIT + 1 })).toContain('仅支持下载')
    expect(previewUnavailableReason({ name: 'report.html', byteSize: 20 })).toContain('文件类型')
    expect(() => decodeTextPreview(new ArrayBuffer(TEXT_PREVIEW_LIMIT + 1))).toThrow('256 KiB')
  })

  it.each([new Uint8Array([0xc3, 0x28]), new Uint8Array([97, 0, 98]), new Uint8Array([27, 91, 49])])('拒绝无效 UTF-8 或二进制控制字符 %#', (bytes) => {
    expect(() => decodeTextPreview(bytes.buffer)).toThrow(/UTF-8|二进制/)
  })

  it('在 DOM 中触发附件，点击后移除链接且延迟释放 URL', () => {
    vi.useFakeTimers()
    const revokeObjectURL = vi.fn()
    vi.stubGlobal('URL', { createObjectURL: vi.fn(() => 'blob:verified'), revokeObjectURL })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      expect(this.isConnected).toBe(true)
      expect(this.hidden).toBe(true)
      expect(this.download).toBe('订单.csv')
      expect(revokeObjectURL).not.toHaveBeenCalled()
    })
    saveBlob(new Blob([text]), '订单.csv')
    expect(click).toHaveBeenCalledOnce()
    expect(document.querySelector('a[download]')).toBeNull()
    vi.advanceTimersByTime(DOWNLOAD_URL_LIFETIME_MS - 1)
    expect(revokeObjectURL).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(revokeObjectURL).toHaveBeenCalledExactlyOnceWith('blob:verified')
  })
})
