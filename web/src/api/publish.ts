// 发布流数据层：媒体直传（预签名 PUT）→ 资产状态轮询 → 两步发布（CreateDraft → PublishDraft）。
// 契约见 proto/bff/v1/media_requests.proto / post_requests.proto。
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { MediaFileType, MediaFormat } from '@gen/media/v1/media_pb.js'
import { unwrap } from '@/api/errors'
import { bff } from '@/api/transport'
import { qk } from '@/api/hooks'

/** 文件 MIME → 契约 MediaFormat（仅 PNG/JPG/WEBP） */
function mediaFormat(type: string): MediaFormat {
  switch (type) {
    case 'image/png':
      return MediaFormat.PNG
    case 'image/jpeg':
      return MediaFormat.JPG
    case 'image/webp':
      return MediaFormat.WEBP
    default:
      throw new Error(`不支持的图片格式：${type}（仅 PNG/JPG/WEBP）`)
  }
}

async function sha256Hex(buffer: ArrayBuffer): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', buffer)
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
}

// Chrome 对空闲页面的 setTimeout 有唤醒节流（Intensive Wake Up Throttling，空闲后 timer 每分钟才醒），
// 发布轮询等待期间页面无交互会近乎停滞；用 Worker 的定时器驱动延时（不受页面节流影响）。
let tickWorker: Worker | null = null
function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    if (!tickWorker) {
      tickWorker = new Worker(URL.createObjectURL(new Blob(['setInterval(() => postMessage(0), 200)'], { type: 'text/javascript' })))
    }
    const start = Date.now()
    const onTick = () => {
      if (Date.now() - start >= ms) {
        tickWorker?.removeEventListener('message', onTick)
        resolve()
      }
    }
    tickWorker.addEventListener('message', onTick)
  })
}

async function imageSize(file: File): Promise<{ width: number; height: number }> {
  const bitmap = await createImageBitmap(file)
  const { width, height } = bitmap
  bitmap.close()
  return { width, height }
}

/** 单文件直传：UploadMedia 签发 URL → 浏览器 PUT 直传对象存储，返回 asset_id */
export async function uploadMedia(file: File): Promise<string> {
  const buffer = await file.arrayBuffer()
  const [hex, size] = await Promise.all([sha256Hex(buffer), imageSize(file)])
  const ok = unwrap(
    await bff.uploadMedia({
      meta: {
        sha256Sum: hex,
        sizeBytes: BigInt(buffer.byteLength),
        format: mediaFormat(file.type),
        width: BigInt(size.width),
        height: BigInt(size.height),
      },
    }),
  )
  const assetId = ok.asset?.base?.id ?? ''
  const res = await fetch(ok.signedUrl, {
    method: 'PUT',
    body: file,
    headers: { 'Content-Type': file.type },
  })
  if (!res.ok) {
    throw new Error(`媒体直传失败（HTTP ${res.status}），请重试`)
  }
  return assetId
}

/** 轮询资产状态 UPLOADING→ACTIVE（决定何时提交写操作）；失败态抛可读错误。
 * 窗口 60s：媒体处理链路 = 上传确认（对象未到时退避重投）+ 三档变体生成（秒级）。
 * 单轮查询加 5s 超时防护：网络挂起时不阻塞整体超时判定。 */
export async function waitAssetsActive(ids: string[]): Promise<void> {
  const deadline = Date.now() + 60_000
  for (;;) {
    let statuses: { fail: boolean; status: number }[] | null = null
    try {
      const ok = await Promise.race([
        bff.getMediaAssetStatus({ ids }).then(unwrap),
        delay(5_000).then(() => Promise.reject(new Error('status poll timeout'))),
      ])
      statuses = ok.statuses
    } catch (e) {
      // 单轮查询失败/超时（瞬时网络抖动）：不放弃，进入下一轮直至整体 deadline
      console.warn('[publish] 资产状态查询本轮失败，将重试', e)
    }
    if (statuses) {
      const failed = statuses.find((s) => s.fail || s.status === 4 /* FAILED */ || s.status === 6 /* ERROR */)
      if (failed) {
        throw new Error('媒体处理失败，请更换图片重试')
      }
      if (statuses.length > 0 && statuses.every((s) => s.status === 3 /* ACTIVE */)) return
    }
    if (Date.now() > deadline) {
      throw new Error('媒体处理超时，请稍后在“我的笔记”查看')
    }
    await delay(1000)
  }
}

export interface PublishInput {
  files: File[]
  title: string
  fullText: string
  /** 话题（存 metadata JSON，契约 metadata 为自由结构） */
  topics: string[]
  /** true=保存草稿后立即发布（DRAFT→AUDITING）；false=仅存草稿 */
  publish: boolean
}

/**
 * 发布链路：直传全部媒体 → 等资产 ACTIVE → CreateDraft（首图即封面）→ 可选 PublishDraft。
 * 返回 postId（草稿/帖子 id）。
 */
export function useSubmitPost() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ files, title, fullText, topics, publish }: PublishInput) => {
      const assetIds: string[] = []
      for (const f of files) {
        assetIds.push(await uploadMedia(f))
      }
      await waitAssetsActive(assetIds)
      const draft = unwrap(
        await bff.createDraft({
          title,
          fullText,
          medias: assetIds.map((assetId) => ({ assetId, type: MediaFileType.MEDIA_FILE_TYPE_ORIGIN })),
          metadata: { payload: JSON.stringify({ topics }) },
        }),
      )
      const postId = draft.post?.postId ?? ''
      if (publish) {
        unwrap(await bff.publishDraft({ postId }))
      }
      return { postId, published: publish }
    },
    onSuccess: () => {
      // 我的笔记/信息流缓存失效，回头可见新内容
      void qc.invalidateQueries({ queryKey: qk.feed('recommend') })
      void qc.invalidateQueries({ queryKey: ['user-posts'] })
    },
  })
}
