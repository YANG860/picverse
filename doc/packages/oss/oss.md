# oss — S3 / MinIO 对象存储

代码：`packages/oss/`（`client.go`、`upload.go`、`download.go`、`presign.go`）。

封装 `minio-go/v7`，提供对象存取与预签名 URL 能力。接口抽象为 `oss.S3Repo`，实现为 `*oss.MinioClient`。

## 初始化

`NewClient(cfg, lf)`：
- 用 `config.S3Config` 建立客户端（静态 V4 凭据）。
- 启动时确保存储桶存在，不存在则创建（fail-fast，失败 panic）。

## 接口能力（S3Repo）

| 方法 | 说明 |
| --- | --- |
| `Upload` | 上传对象（`PutObject`），返回 `UploadResult{Key, ETag, Size}` |
| `Download` | 下载对象，返回 `DownloadResult{ContentType, Size, ETag, Body}` |
| `DownloadToWriter` | 下载到 `io.Writer`，返回写入字节数 |
| `Exist` | 检查对象是否存在（`NoSuchKey` 等返回 false） |
| `DeleteObject` / `DeleteObjects` | 单个 / 批量删除对象 |
| `PresignedPutURL` | 预签名上传 URL |
| `PresignedGetURL` | 预签名下载 URL |
| `PresignedDeleteURL` | 预签名删除 URL |
| `PresignedURL` | 通用预签名（GET/PUT/DELETE/HEAD） |
| `Bucket` | 返回存储桶名 |
| `HealthCheck` | 健康检查（`BucketExists`） |

## 预签名 URL

`PresignedURLResult{URL, ExpiresAt}`。`UploadMedia` 用 `PresignedPutURL` 让客户端**直传 MinIO**，避免大文件经服务端。默认过期 `packages/defaults.PutUrlExpiry`（15 分钟）。

> `Upload`/`Presign` 系列中构造的 `reqParams`（如 Content-Type 约束）当前未实际作用于 minio 的 `PresignedPutObject`，属预留。

## 对象键

对象键由 [packages/utils](../utils/utils.md) 的 `BuildKey` 生成：`media/<id>/<type>.<ext>`。
