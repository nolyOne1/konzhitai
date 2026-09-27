export const DOWNLOAD_URL_LIFETIME_MS = 30_000
export const TEXT_PREVIEW_LIMIT = 256 * 1024

type DownloadKind = 'artifact' | 'archive' | 'logs'
export type FileDigest = { byteSize: number; sha256: string }
export class FileIntegrityError extends Error {}

// Keep the URL alive while the browser starts consuming the download. The
// bounded timer also releases it after this page has been unmounted.
export function saveBlob(body: Blob, filename: string) {
  const url = URL.createObjectURL(body)
  const revoke = URL.revokeObjectURL.bind(URL)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.hidden = true
  try {
    document.body.append(anchor)
    anchor.click()
  } finally {
    anchor.remove()
    window.setTimeout(() => revoke(url), DOWNLOAD_URL_LIFETIME_MS)
  }
}

export function validateDownloadType(body: Blob, kind: DownloadKind) {
  const expected = { artifact: 'application/octet-stream', archive: 'application/gzip', logs: 'text/plain' }
  if (body.type.split(';', 1)[0].trim().toLowerCase() !== expected[kind]) {
    throw new Error('下载响应类型不正确，可能返回了错误页面，请重试。')
  }
}

export async function verifyFile(body: Blob, expected: FileDigest, kind: 'artifact' | 'archive'): Promise<ArrayBuffer> {
  validateDownloadType(body, kind)
  if (!Number.isSafeInteger(expected.byteSize) || expected.byteSize < 0 || !/^[a-f0-9]{64}$/i.test(expected.sha256)) {
    throw new Error('文件校验信息不完整，请刷新后重试。')
  }
  if (body.size !== expected.byteSize) throw new FileIntegrityError('文件大小校验失败，已停止下载或预览，请重试。')
  if (!globalThis.crypto?.subtle) throw new Error('当前浏览器无法校验文件，请使用支持安全连接的浏览器重试。')
  const bytes = await body.arrayBuffer()
  const digest = await crypto.subtle.digest('SHA-256', bytes)
  const actual = Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, '0')).join('')
  if (actual !== expected.sha256.toLowerCase()) throw new FileIntegrityError('文件 SHA-256 校验失败，已停止下载或预览，请重试。')
  return bytes
}

export function previewUnavailableReason(file: { name: string; byteSize: number }): string {
  if (file.byteSize > TEXT_PREVIEW_LIMIT) return '超过 256 KiB，仅支持下载。'
  if (!/\.(txt|csv|tsv|json|jsonl|ndjson|log|md|yaml|yml)$/i.test(file.name)) return '此文件类型仅支持下载。'
  return ''
}

export function decodeTextPreview(bytes: ArrayBuffer): string {
  if (bytes.byteLength > TEXT_PREVIEW_LIMIT) throw new Error('超过 256 KiB，仅支持下载。')
  let text: string
  try { text = new TextDecoder('utf-8', { fatal: true }).decode(bytes) }
  catch { throw new Error('文件不是有效的 UTF-8 文本，请下载后使用合适的软件打开。') }
  if (/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/u.test(text)) {
    throw new Error('文件包含二进制控制字符，仅支持下载。')
  }
  return text
}
