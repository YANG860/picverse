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

### 签名域名（`public_endpoint`）

SigV4 的 canonical request 含 `Host`，**签名与域名强绑定**——把已签名 URL 的 host 换成 CDN 域名会让签名失效（`SignatureDoesNotMatch`）。所以签名必须由「以对外域名为 endpoint」的 client 完成：

| 配置 | 作用 |
| --- | --- |
| `s3.endpoint` / `s3.use_ssl` | 服务端自身 Upload/Download/Delete 走的地址（内网直连，如 `106.53.172.186:9000`） |
| `s3.public_endpoint` / `s3.public_use_ssl` | **仅预签名** 使用的对外域名（如 `media.picverse.site` + https）。留空则回落 `endpoint` |

实现上 `MinioClient` 持有两个 client：内嵌 `Client`（内网）与 `presign`（对外域名），四个 `Presigned*` 方法用后者。对外域名前置的反代/CDN/隧道必须**保留原始 `Host` 头**转发到 MinIO（Caddy/Nginx/Cloudflare Tunnel 默认行为），否则 MinIO 侧按 `Host` 重算签名会不匹配。

预签名 URL 形如 `https://media.picverse.site/rednote/uploads/<asset>/origin.jpg?X-Amz-...`（path-style，bucket 在路径里），反代需把整个路径透传给 MinIO。

本项目落地（Cloudflare Tunnel，与站点同一条 locally-managed tunnel）：

```yaml
# /etc/cloudflared/config.yml
ingress:
  - hostname: media.picverse.site
    service: http://localhost:9000     # MinIO；cloudflared 原样转发 Host
  - hostname: picverse.site
    service: http://localhost:8088
  - service: http_status:404
```

DNS 必须是 **`CNAME → <tunnel-id>.cfargotunnel.com`（代理开启）**：若写成指向服务器 IP 的 A 记录，Cloudflare 会按 443 回源而不是交给隧道，表现为 `HTTP 521`（源站不可达），与本配置无解耦关系。

上线自检（三条都过才算通）：

| 检查 | 期望 |
| --- | --- |
| `curl https://media.picverse.site/minio/health/live` | `200`（隧道→MinIO 通） |
| 用该域名签出的 GET URL 直接请求已存在对象 | `200` + 正确字节数（Host 参与签名仍匹配） |
| 同上用 PUT URL 上传后 `DELETE` | `200` / `204`（客户端直传路径通） |

前置条件未就绪时**不要**提前配 `public_endpoint`：签名域名一旦启用，前端上传立即指向该域名，DNS/隧道没通就是 521 全量失败。

> `Upload`/`Presign` 系列中构造的 `reqParams`（如 Content-Type 约束）当前未实际作用于 minio 的 `PresignedPutObject`，属预留。

## 对象键

对象键由 [packages/utils](../utils/utils.md) 的 `BuildKey` 生成：`media/<id>/<type>.<ext>`。
